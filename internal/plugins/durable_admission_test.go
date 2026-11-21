package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/resourceguard"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/require"
)

func admissionUsage(d *DurableEvents) (int64, int64) {
	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	return d.admissionRecords, d.admissionBytes
}

func startAdmissionCaptures(t *testing.T, d *DurableEvents, count, bodySize int) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() { e := durableTestEvent(); e.Body = strings.Repeat("x", bodySize); results <- d.Enqueue(ctx, e) })
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("durable admissions did not finish")
		}
	})

	return results, cancel
}

func TestDurableAdmissionBoundsConnectedLargeCaptures(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "full"
		if unavailable {
			name = "unavailable"
		}

		t.Run(name, func(t *testing.T) {
			d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 32<<20, 1)
			d.memoryCheck = func() error { return nil }
			seed := durableTestEvent()
			require.NoError(t, d.Enqueue(context.Background(), seed))
			original, err := os.ReadFile(filepath.Join(d.directory, seed.EventKey+".json"))
			require.NoError(t, err)
			var calls atomic.Int64
			var apiRecovered atomic.Bool

			d.post = func(context.Context, []byte) (eventPostResult, error) {
				calls.Add(1)
				if apiRecovered.Load() {
					return eventPostResult{status: 200}, nil
				}

				return eventPostResult{status: 503}, nil
			}

			if unavailable {
				d.maxRecords = 100
				d.writeFile = func(string, []byte) error { return os.ErrPermission }
			}

			d.Start()
			const count = 96
			event := durableTestEvent()
			event.Body = strings.Repeat("x", 1<<20)
			allowed := int(DurableCaptureMaxBytes / admissionSize(event))
			require.Less(t, allowed, count)
			results, cancel := startAdmissionCaptures(t, d, count, 1<<20)

			for range count - allowed {
				select {
				case err := <-results:
					require.ErrorIs(t, err, ErrDurableCaptureCapacity)
				case <-time.After(3 * time.Second):
					t.Fatal("excess connected capture did not reject promptly")
				}
			}

			records, bytes := admissionUsage(d)
			require.Equal(t, int64(allowed), records)
			require.LessOrEqual(t, bytes, DurableCaptureMaxBytes)
			require.Eventually(t, func() bool { return calls.Load() > 0 }, time.Second, time.Millisecond)
			retained, err := os.ReadFile(filepath.Join(d.directory, seed.EventKey+".json"))
			require.NoError(t, err)
			require.Equal(t, original, retained)
			cancel()

			for range allowed {
				select {
				case err := <-results:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("cancellation retained durable capture")
				}
			}

			records, bytes = admissionUsage(d)
			require.Zero(t, records)
			require.Zero(t, bytes)

			// Storage/API recovery can admit new events after the budget is released.
			d.mu.Lock()
			d.writeFile = writeEventFile
			d.mu.Unlock()
			apiRecovered.Store(true)
			eventuallyEmpty(t, d.directory)
			require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
		})
	}
}

func TestDurableAdmissionRecordBoundPrecedesEncoding(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 1)
	d.memoryCheck = func() error { return nil }
	seed := durableTestEvent()
	require.NoError(t, d.Enqueue(context.Background(), seed))
	results, cancel := startAdmissionCaptures(t, d, int(DurableCaptureMaxRecords), 0)
	require.Eventually(t, func() bool { n, _ := admissionUsage(d); return n == DurableCaptureMaxRecords }, 3*time.Second, time.Millisecond)

	// An invalid key would fail before encoding without the admission gate.
	// Capacity must be rejected before event conversion/serialization even begins.
	require.ErrorIs(t, d.Enqueue(context.Background(), tracer.Event{EventKey: "invalid"}), ErrDurableCaptureCapacity)
	cancel()
	for range DurableCaptureMaxRecords {
		require.ErrorIs(t, <-results, context.Canceled)
	}

	n, b := admissionUsage(d)
	require.Zero(t, n)
	require.Zero(t, b)
	require.NoError(t, d.Enqueue(context.Background(), seed))
}

func TestDurableAdmissionReleasesOnShutdownAndPressure(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "pressure"
		if shutdown {
			name = "shutdown"
		}

		t.Run(name, func(t *testing.T) {
			d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 1)
			var pressure atomic.Bool

			d.memoryCheck = func() error {
				if pressure.Load() {
					return resourceguard.ErrMemoryPressure
				}
				return nil
			}

			seed := durableTestEvent()
			require.NoError(t, d.Enqueue(context.Background(), seed))
			d.post = func(context.Context, []byte) (eventPostResult, error) { return eventPostResult{status: 503}, nil }
			results, _ := startAdmissionCaptures(t, d, 3, 1024)
			require.Eventually(t, func() bool { n, _ := admissionUsage(d); return n == 3 }, time.Second, time.Millisecond)
			want := resourceguard.ErrMemoryPressure

			if shutdown {
				d.Close()
				want = ErrEventSpoolClosed
			} else {
				pressure.Store(true)
			}

			for range 3 {
				select {
				case err := <-results:
					require.ErrorIs(t, err, want)
				case <-time.After(time.Second):
					t.Fatal("admission budget retained after pressure/shutdown")
				}
			}

			n, b := admissionUsage(d)
			require.Zero(t, n)
			require.Zero(t, b)
			if !shutdown {
				require.ErrorIs(t, d.Enqueue(context.Background(), seed), resourceguard.ErrMemoryPressure)
				pressure.Store(false)
				require.NoError(t, d.Enqueue(context.Background(), seed))
			}
		})
	}
}

func TestDurableAdmissionSizeIncludesEscapingAndMetadata(t *testing.T) {
	for _, s := range []string{strings.Repeat("x", 70000), strings.Repeat("\x00\"\\<>&", 4096), strings.Repeat("\xff", 4096)} {
		e := durableTestEvent()
		e.Body = s
		e.Headers = s
		e.Metadata = map[string]string{s: s}
		e.HeadersMap = map[string][]string{"synthetic": {s}}
		dto, err := (&beelzebubCloud{}).mapToEventDTO(e)
		require.NoError(t, err)
		payload, err := json.Marshal(dto)
		require.NoError(t, err)
		envelope, err := json.Marshal(spoolRecord{Version: 1, EventKey: e.EventKey, Destination: strings.Repeat("d", 64), Payload: payload})
		require.NoError(t, err)
		require.Greater(t, admissionSize(e), int64(len(s)*5+len(dto.Headers)+len(payload)+len(envelope)))
	}

	e := durableTestEvent()
	e.Body = strings.Repeat("x", int(DurableCaptureMaxBytes/16))
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	require.ErrorIs(t, d.Enqueue(context.Background(), e), ErrDurableCaptureCapacity)
	n, b := admissionUsage(d)
	require.Zero(t, n)
	require.Zero(t, b)
	e.Body = ""
	e.Headers = strings.Repeat("x", int(DurableCaptureMaxBytes/64))
	require.Greater(t, admissionSize(e), DurableCaptureMaxBytes)
	e.Headers = ""
	e.HeadersMap = map[string][]string{"synthetic": make([]string, int(DurableCaptureMaxBytes/64))}
	require.Greater(t, admissionSize(e), DurableCaptureMaxBytes, "empty values still retain slice entries")
	e.HeadersMap = nil
	e.Metadata = map[string]string{"synthetic": strings.Repeat("x", int(DurableCaptureMaxBytes/16))}
	require.Greater(t, admissionSize(e), DurableCaptureMaxBytes)
}

func TestDurableAdmissionReleasesOnValidationAndDiskBudgetErrors(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 1, 1)
		e := durableTestEvent()
		want := ErrEventTooLarge
		if invalid {
			e.EventKey = "invalid"
		}

		err := d.Enqueue(context.Background(), e)
		if invalid {
			require.ErrorContains(t, err, "invalid durable event key")
		} else {
			require.True(t, errors.Is(err, want))
		}

		n, b := admissionUsage(d)
		require.Zero(t, n)
		require.Zero(t, b)
	}
}

func TestDurableAdmissionShutdownDuringReservation(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	d.memoryCheck = func() error {
		d.Close()
		return nil
	}

	require.ErrorIs(t, d.Enqueue(context.Background(), durableTestEvent()), ErrEventSpoolClosed)
	n, b := admissionUsage(d)
	require.Zero(t, n)
	require.Zero(t, b)
}
