package tracer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDurableCaptureWaitsBeforeAcceptance(t *testing.T) {
	tr, counters := newTracerWithMockCounters(func(Event) { t.Error("durable capture entered volatile strategy") })
	entered := make(chan Event, 1)
	persisted := make(chan struct{})
	tr.SetDurableSink(func(_ context.Context, event Event) error { entered <- event; <-persisted; return nil })
	done := make(chan struct{})
	go func() { tr.TraceEvent(Event{ID: "synthetic-session", Protocol: "SSH"}); close(done) }()
	event := <-entered
	require.NotEmpty(t, event.EventKey)
	key, err := uuid.Parse(event.EventKey)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), key.Version())
	require.Equal(t, "synthetic-session", event.ID)
	_, err = time.Parse(time.RFC3339Nano, event.DateTime)
	require.NoError(t, err)

	select {
	case <-done:
		t.Fatal("capture returned before persistence")
	default:
	}

	close(persisted)
	<-done
	require.Equal(t, 1, counters["total"].count)
	require.Empty(t, tr.eventsChan)
}

func TestDurableCaptureMonotonicKeysAndExistingIdentity(t *testing.T) {
	tr, _ := newTracerWithMockCounters(func(Event) { t.Error("used volatile strategy") })
	var keys []uuid.UUID
	tr.SetDurableSink(func(_ context.Context, event Event) error {
		key, err := uuid.Parse(event.EventKey)
		require.NoError(t, err)
		keys = append(keys, key)
		return nil
	})

	for range 512 {
		require.NoError(t, tr.TraceEventContext(context.Background(), Event{}))
	}

	sameMillisecond := false
	for i, key := range keys {
		require.Equal(t, uuid.Version(7), key.Version())
		if i > 0 {
			require.Less(t, keys[i-1].String(), key.String())
			sameMillisecond = sameMillisecond || keys[i-1].Time() == key.Time()
		}
	}

	require.True(t, sameMillisecond, "exercise ordering within a millisecond")
	existing := uuid.New()
	require.NoError(t, tr.TraceEventContext(context.Background(), Event{EventKey: existing.String()}))
	require.Equal(t, existing, keys[len(keys)-1], "never rewrite a supplied key")
}

func TestDurableCaptureFailureIsNotCounted(t *testing.T) {
	tr, counters := newTracerWithMockCounters(func(Event) {})
	failure := errors.New("synthetic storage failure")
	tr.SetDurableSink(func(context.Context, Event) error { return failure })
	require.ErrorIs(t, tr.TraceEventContext(context.Background(), Event{}), failure)
	require.Zero(t, counters["total"].count)
	require.Empty(t, tr.eventsChan)
}

func TestLegacyEventWireShapeUnchanged(t *testing.T) {
	body, err := json.Marshal(Event{ID: "synthetic-session"})
	require.NoError(t, err)
	require.NotContains(t, string(body), "EventKey")
}

func TestDurableTracerDoesNotChangeLegacySingleton(t *testing.T) {
	base := GetInstance(func(Event) {})
	durable := NewDurableTracer(func(context.Context, Event) error { return nil })
	require.NotSame(t, base, durable)
	require.Nil(t, base.durableSink)
}

func TestWithContextCancelsDurableCapture(t *testing.T) {
	tr, counters := newTracerWithMockCounters(func(Event) { t.Error("used legacy capture") })
	entered := make(chan struct{})
	result := make(chan error, 1)
	tr.SetDurableSink(func(ctx context.Context, event Event) error {
		close(entered)
		<-ctx.Done()
		result <- ctx.Err()
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { WithContext(ctx, tr).TraceEvent(Event{}); close(done) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("context-aware capture retained producer")
	}

	require.ErrorIs(t, <-result, context.Canceled)
	require.Zero(t, counters["total"].count)
}

type legacyCapture struct{ calls int }

func (l *legacyCapture) TraceEvent(Event) { l.calls++ }

func TestWithContextSupportsLegacyImplementations(t *testing.T) {
	capture := &legacyCapture{}
	WithContext(context.Background(), capture).TraceEvent(Event{})
	require.Equal(t, 1, capture.calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, WithContext(ctx, capture).(ContextTracer).TraceEventContext(ctx, Event{}), context.Canceled)
	require.Equal(t, 1, capture.calls)
}

func TestFinalCaptureDetachesExpiredContextAndRemainsBounded(t *testing.T) {
	type contextKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "synthetic"))
	cancel()
	var result error
	capture := NewDurableTracer(func(ctx context.Context, event Event) error {
		require.NoError(t, ctx.Err())
		require.Equal(t, "synthetic", ctx.Value(contextKey{}))
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), time.Duration(0))
		require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
		<-ctx.Done()
		result = ctx.Err()
		return result
	})

	start := time.Now()
	TraceFinalEvent(parent, WithContext(parent, capture), Event{Status: End.String()})
	require.ErrorIs(t, result, context.DeadlineExceeded)
	require.GreaterOrEqual(t, time.Since(start), 4500*time.Millisecond)
	require.Less(t, time.Since(start), 7*time.Second)
}

func TestFinalCapturePreservesHealthyLegacyTracer(t *testing.T) {
	capture := &legacyCapture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	TraceFinalEvent(ctx, WithContext(ctx, capture), Event{Status: End.String()})
	require.Equal(t, 1, capture.calls)
}

func TestLegacyDeliveryWithoutDurableSink(t *testing.T) {
	delivered := make(chan Event, 1)
	legacy := GetInstance(func(Event) {})
	previous := legacy.GetStrategy()
	legacy.SetStrategy(func(event Event) { delivered <- event })
	t.Cleanup(func() { legacy.SetStrategy(previous) })
	require.Nil(t, legacy.durableSink)
	require.NoError(t, legacy.TraceEventContext(context.Background(), Event{ID: "synthetic-legacy", Protocol: "HTTP"}))

	select {
	case event := <-delivered:
		require.Equal(t, "synthetic-legacy", event.ID)
		require.Empty(t, event.EventKey, "legacy mode keeps its original wire shape")
		_, err := time.Parse(time.RFC3339, event.DateTime)
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("legacy delivery stopped without a disk queue")
	}
}

func TestLegacyQueueCancellationDoesNotCountUnacceptedEvent(t *testing.T) {
	tr, counters := newTracerWithMockCounters(func(Event) {})
	for range cap(tr.eventsChan) {
		require.NoError(t, tr.TraceEventContext(context.Background(), Event{ID: "accepted"}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, tr.TraceEventContext(ctx, Event{ID: "unaccepted"}), context.DeadlineExceeded)
	require.Equal(t, cap(tr.eventsChan), counters["total"].count)
	require.Len(t, tr.eventsChan, cap(tr.eventsChan))
	require.ErrorIs(t, tr.TraceEventContext(ctx, Event{}), context.DeadlineExceeded)
}

func TestCaptureWithNilProducerContext(t *testing.T) {
	capture := &legacyCapture{}
	WithContext(nil, capture).TraceEvent(Event{})
	TraceFinalEvent(nil, capture, Event{})
	require.Equal(t, 2, capture.calls)
}
