package tracer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/resourceguard"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func capacityTracer() *tracer {
	return &tracer{
		eventsChan:           make(chan Event, Workers),
		eventsTotal:          prometheus.NewCounter(prometheus.CounterOpts{Name: "synthetic_accepted"}),
		eventsHTTPTotal:      prometheus.NewCounter(prometheus.CounterOpts{Name: "synthetic_http"}),
		captureFailuresTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: "synthetic_rejected"}),
		legacyStallTimeout:   500 * time.Millisecond,
	}
}

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	var metric dto.Metric
	require.NoError(t, counter.Write(&metric))
	return metric.GetCounter().GetValue()
}

func TestLegacyWaitingCapacityRemainsBoundedWithDeliveryProgress(t *testing.T) {
	tr := capacityTracer()
	for range Workers {
		tr.eventsChan <- Event{ID: "seed"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, LegacyQueueMaxWaiters)
	var producers sync.WaitGroup
	for i := range LegacyQueueMaxWaiters {
		producers.Go(func() { results <- tr.TraceEventContext(ctx, Event{ID: fmt.Sprint(i), Protocol: HTTP.String()}) })
	}

	done := make(chan struct{})
	go func() { producers.Wait(); close(done) }()
	progress, cancelProgress := context.WithCancel(context.Background())
	defer cancelProgress()

	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-progress.Done():
				return
			case <-ticker.C:
				tr.legacyDequeued.Add(1)
			}
		}
	}()

	t.Cleanup(func() {
		cancelProgress()
		timeout := time.NewTimer(3 * time.Second)
		defer timeout.Stop()

		for {
			select {
			case <-done:
				return
			case <-tr.eventsChan:
			case <-timeout.C:
				t.Error("waiting admissions did not release")
				return
			}
		}
	})

	require.Eventually(t, func() bool { return tr.legacyWaiters.Load() == LegacyQueueMaxWaiters }, time.Second, time.Millisecond)
	cancel() // Clients can disconnect after they start waiting.
	// Model dequeues immediately refilled by competing traffic. Waiting lasts
	// more than two stall budgets while the queue continues making progress.

	select {
	case err := <-results:
		t.Fatalf("bounded burst was rejected: %v", err)
	case <-time.After(1100 * time.Millisecond):
	}

	require.Equal(t, int64(LegacyQueueMaxWaiters), tr.legacyWaiters.Load())
	const excess = 64
	rejected := make(chan error, excess)
	var wave sync.WaitGroup

	for range excess {
		wave.Go(func() { rejected <- tr.TraceEventContext(ctx, Event{ID: "excess", Protocol: HTTP.String()}) })
	}

	wave.Wait()
	close(rejected)
	for err := range rejected {
		require.ErrorIs(t, err, ErrLegacyQueueCapacity)
	}

	require.Equal(t, float64(excess), counterValue(t, tr.captureFailuresTotal))
	require.Zero(t, counterValue(t, tr.eventsTotal))
	require.Equal(t, int64(LegacyQueueMaxWaiters), tr.legacyWaiters.Load())
	seen := map[string]int{}
	for range Workers + LegacyQueueMaxWaiters {
		select {
		case event := <-tr.eventsChan:
			seen[event.ID]++
			tr.legacyDequeued.Add(1)
		case <-time.After(time.Second):
			t.Fatal("bounded burst did not drain")
		}
	}
	<-done

	for range LegacyQueueMaxWaiters {
		require.NoError(t, <-results)
	}

	require.Equal(t, Workers, seen["seed"])
	require.Zero(t, seen["excess"])
	for i := range LegacyQueueMaxWaiters {
		require.Equal(t, 1, seen[fmt.Sprint(i)])
	}

	require.Zero(t, tr.legacyWaiters.Load())
	require.Equal(t, float64(LegacyQueueMaxWaiters), counterValue(t, tr.eventsTotal))
	require.NoError(t, tr.TraceEventContext(ctx, Event{ID: "recovered", Protocol: HTTP.String()}))

	require.Equal(t, "recovered", (<-tr.eventsChan).ID)
}

func TestLegacyMemoryPressureReleasesWaitersAndRecovers(t *testing.T) {
	tr := capacityTracer()
	for range Workers {
		tr.eventsChan <- Event{ID: "seed"}
	}

	var pressure atomic.Bool
	checked := make(chan struct{})
	var checks atomic.Int64

	tr.memoryCheck = func() error {
		high := pressure.Load()
		if checks.Add(1) == 1 {
			close(checked)
		}

		if high {
			return resourceguard.ErrMemoryPressure
		}

		return nil
	}

	result := make(chan error, 1)
	go func() {
		result <- tr.TraceEventContext(context.Background(), Event{ID: "waiting", Protocol: HTTP.String()})
	}()

	require.Eventually(t, func() bool { return tr.legacyWaiters.Load() == 1 }, time.Second, time.Millisecond)
	<-checked
	pressure.Store(true)

	select {
	case err := <-result:
		require.ErrorIs(t, err, resourceguard.ErrMemoryPressure)
	case <-time.After(time.Second):
		t.Fatal("memory pressure retained waiter")
	}

	require.Zero(t, tr.legacyWaiters.Load())
	require.Equal(t, float64(1), counterValue(t, tr.captureFailuresTotal))
	require.ErrorIs(t, tr.TraceEventContext(context.Background(), Event{Protocol: HTTP.String()}), resourceguard.ErrMemoryPressure)
	require.Zero(t, tr.legacyWaiters.Load())
	pressure.Store(false)
	<-tr.eventsChan
	require.NoError(t, tr.TraceEventContext(context.Background(), Event{ID: "recovered", Protocol: HTTP.String()}))
	require.Equal(t, float64(1), counterValue(t, tr.eventsTotal))
	require.Equal(t, float64(2), counterValue(t, tr.captureFailuresTotal))
}

func TestLegacyWaiterCapDoesNotBlockAvailableQueueOrDurableCapture(t *testing.T) {
	tr := capacityTracer()
	tr.legacyWaiters.Store(LegacyQueueMaxWaiters)
	var checks int
	tr.memoryCheck = func() error { checks++; return nil }
	require.NoError(t, tr.TraceEventContext(context.Background(), Event{Protocol: HTTP.String()}))
	require.Len(t, tr.eventsChan, 1)
	require.Equal(t, 1, checks)
	var captured int
	tr.SetDurableSink(func(context.Context, Event) error { captured++; return nil })
	require.NoError(t, tr.TraceEventContext(context.Background(), Event{Protocol: HTTP.String()}))
	require.Equal(t, 1, captured)
	require.Equal(t, 1, checks, "durable capture must bypass the memory-only admission guard")
	require.Equal(t, int64(LegacyQueueMaxWaiters), tr.legacyWaiters.Load())
}

func TestLegacyMemoryPressureRejectsAvailableQueueAndRecovers(t *testing.T) {
	tr := capacityTracer()
	tr.memoryCheck = func() error { return resourceguard.ErrMemoryPressure }
	require.ErrorIs(t, tr.TraceEventContext(context.Background(), Event{Protocol: HTTP.String()}), resourceguard.ErrMemoryPressure)
	require.Empty(t, tr.eventsChan)
	require.Zero(t, tr.legacyWaiters.Load())
	require.Zero(t, counterValue(t, tr.eventsTotal))
	require.Equal(t, float64(1), counterValue(t, tr.captureFailuresTotal))
	tr.memoryCheck = func() error { return nil }
	require.NoError(t, tr.TraceEventContext(context.Background(), Event{Protocol: HTTP.String()}))
	require.Len(t, tr.eventsChan, 1)
}
