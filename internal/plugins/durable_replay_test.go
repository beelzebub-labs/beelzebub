package plugins

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func orderedTestEvent(milli int64, sequence uint16) tracer.Event {
	var key uuid.UUID
	binary.BigEndian.PutUint64(key[:8], uint64(milli)<<16|0x7000|uint64(sequence))
	key[8] = 0x80
	event := durableTestEvent()
	event.EventKey = key.String()
	event.DateTime = time.UnixMilli(milli).UTC().Format(time.RFC3339Nano)
	return event
}

func replayRecorder(t *testing.T, d *DurableEvents) *[]EventDTO {
	t.Helper()
	var posted []EventDTO
	d.post = func(_ context.Context, body []byte) (eventPostResult, error) {
		var event EventDTO
		if err := json.Unmarshal(body, &event); err != nil {
			t.Error(err)
			return eventPostResult{}, err
		}

		posted = append(posted, event)
		return eventPostResult{status: 200, key: event.EventKey, keyPresent: true}, nil
	}

	return &posted // Read only after Close has joined the delivery worker.
}

func TestDurableEventsGeneratesV7AndPreservesExistingKeys(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	event := durableTestEvent()
	event.EventKey = ""
	require.NoError(t, d.Enqueue(context.Background(), event))
	paths := pendingFiles(t, d.directory)
	record, err := d.readRecord(paths[0])
	require.NoError(t, err)
	key, err := uuid.Parse(record.EventKey)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), key.Version())
	existing := durableTestEvent()
	require.NoError(t, d.Enqueue(context.Background(), existing))
	require.FileExists(t, filepath.Join(d.directory, existing.EventKey+".json"))
}

func TestDurableReplaySameMillisecondOrderAcrossRestart(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	var want []string
	for i := 19; i >= 0; i-- {
		event := orderedTestEvent(base, uint16(i))
		event.Status = []string{"Start", "Interaction", "End"}[min(i, 2)]
		require.NoError(t, d.Enqueue(context.Background(), event))
		want = append(want, event.EventKey)
	}

	d.Close()
	slices.Sort(want)
	d = testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	posted := replayRecorder(t, d)
	d.Start()
	eventuallyEmpty(t, directory)
	d.Close()
	var got []string
	for _, event := range *posted {
		got = append(got, event.EventKey)
	}

	require.Equal(t, want, got)
	require.Equal(t, "Start", (*posted)[0].Status)
	require.Equal(t, "End", (*posted)[len(*posted)-1].Status)
}

func TestDurableReplayMixedLegacyAndV7(t *testing.T) {
	directory := t.TempDir()
	d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	v7 := orderedTestEvent(base+2, 0)
	v4 := durableTestEvent()
	v4.DateTime = time.UnixMilli(base + 1).Format(time.RFC3339Nano)
	require.NoError(t, d.Enqueue(context.Background(), v7))
	require.NoError(t, d.Enqueue(context.Background(), v4))
	original, err := os.ReadFile(filepath.Join(directory, v4.EventKey+".json"))
	require.NoError(t, err)
	d.Close()
	raw := durableTestEvent()
	raw.DateTime = time.UnixMilli(base).Format(time.RFC3339Nano)
	body, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "20260101T000000.000000000-"+raw.EventKey+".json"), body, 0600))
	d = testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	posted := replayRecorder(t, d)
	retained, err := os.ReadFile(filepath.Join(directory, v4.EventKey+".json"))
	require.NoError(t, err)
	require.Equal(t, original, retained, "ordering must not rewrite stored records")
	d.Start()
	eventuallyEmpty(t, directory)
	d.Close()
	require.Len(t, *posted, 3)
	require.Equal(t, []string{raw.EventKey, v4.EventKey, v7.EventKey}, []string{(*posted)[0].EventKey, (*posted)[1].EventKey, (*posted)[2].EventKey})
}

func TestDurableReplaySelectsGloballyOldestBoundedBatches(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	var want []string
	for i := 29; i >= 0; i-- {
		event := orderedTestEvent(base+int64(i), 0)
		require.NoError(t, d.Enqueue(context.Background(), event))
		want = append(want, event.EventKey)
	}

	slices.Sort(want)
	posted := replayRecorder(t, d)
	var got []string
	for {
		batch, err := d.oldestPendingEvents(4)
		require.NoError(t, err)
		require.LessOrEqual(t, len(batch), 4)
		if len(batch) == 0 {
			break
		}

		for _, record := range batch {
			got = append(got, record.key)
			_, complete := d.deliver(filepath.Join(d.directory, record.name))
			require.True(t, complete)
		}
	}

	require.Equal(t, want, got)
	require.Len(t, *posted, len(want))
	d.cancel()
	_, err := d.oldestPendingEvents(4)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDurableReplayConcurrentCaptureRetriesHeadBeforeAdvancing(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	first, second := orderedTestEvent(base, 0), orderedTestEvent(base+1, 0)
	require.NoError(t, d.Enqueue(context.Background(), second))
	require.NoError(t, d.Enqueue(context.Background(), first))
	entered, recovery := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-recovery:
		default:
			close(recovery)
		}
	})

	var keys []string
	var firstBodies []string
	d.post = func(_ context.Context, body []byte) (eventPostResult, error) {
		var event EventDTO
		if err := json.Unmarshal(body, &event); err != nil {
			return eventPostResult{}, err
		}

		keys = append(keys, event.EventKey)
		if len(keys) == 1 {
			close(entered)
			<-recovery
		}

		if event.EventKey == first.EventKey {
			firstBodies = append(firstBodies, string(body))
			if len(firstBodies) <= 4 {
				return eventPostResult{status: []int{500, 502, 503, 504}[len(firstBodies)-1]}, nil
			}

			if len(firstBodies) == 5 {
				return eventPostResult{}, errors.New("lost acknowledgement after acceptance")
			}
		}

		return eventPostResult{status: 200, key: event.EventKey, keyPresent: true}, nil
	}

	d.Start()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("delivery did not begin")
	}

	var producers sync.WaitGroup
	var want []string
	for i := range 24 {
		event := orderedTestEvent(base+int64(i)+2, 0)
		want = append(want, event.EventKey)
		producers.Go(func() {
			if err := d.Enqueue(context.Background(), event); err != nil {
				t.Error(err)
			}
		})
	}

	producers.Wait()
	close(recovery)
	eventuallyEmpty(t, d.directory)
	d.Close()
	slices.Sort(want)
	want = append([]string{first.EventKey, first.EventKey, first.EventKey, first.EventKey, first.EventKey, first.EventKey, second.EventKey}, want...)
	require.Equal(t, want, keys)
	require.Len(t, firstBodies, 6)

	for _, body := range firstBodies {
		require.Equal(t, firstBodies[0], body, "retries retain exact wire bytes")
	}
}

func TestDurableReplayCorruptRecordsDoNotBlockFollowingEvents(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	require.NoError(t, os.WriteFile(filepath.Join(d.directory, "invalid.json"), []byte("corrupt"), 0600))
	event := orderedTestEvent(time.Now().UnixMilli(), 0)
	require.NoError(t, d.Enqueue(context.Background(), event))
	posted := replayRecorder(t, d)
	d.Start()
	eventuallyEmpty(t, d.directory)
	d.Close()
	require.Len(t, *posted, 1)
	require.Equal(t, event.EventKey, (*posted)[0].EventKey)
	quarantined, err := filepath.Glob(filepath.Join(d.directory, "quarantine", "invalid.json.*"))
	require.NoError(t, err)
	require.Len(t, quarantined, 1)
}

func TestDurableReplayPermanentRejectionAllowsNextRecord(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	base := time.Now().UnixMilli()
	first, second := orderedTestEvent(base, 0), orderedTestEvent(base+1, 0)
	require.NoError(t, d.Enqueue(context.Background(), second))
	require.NoError(t, d.Enqueue(context.Background(), first))
	var keys []string

	d.post = func(_ context.Context, body []byte) (eventPostResult, error) {
		var event EventDTO

		if err := json.Unmarshal(body, &event); err != nil {
			return eventPostResult{}, err
		}
		keys = append(keys, event.EventKey)

		if event.EventKey == first.EventKey {
			return eventPostResult{status: 422}, nil
		}

		return eventPostResult{status: 200, keyPresent: true, key: event.EventKey}, nil
	}

	d.Start()
	eventuallyEmpty(t, d.directory)
	d.Close()
	require.Equal(t, []string{first.EventKey, second.EventKey}, keys)
	quarantined, err := filepath.Glob(filepath.Join(d.directory, "quarantine", first.EventKey+".json.http-422.*"))
	require.NoError(t, err)
	require.Len(t, quarantined, 1)
	record, err := d.readRecord(quarantined[0])
	require.NoError(t, err)
	require.Equal(t, first.EventKey, record.EventKey)
}

func TestDurableReplayOldestSelectionWithAdversarialDirectoryOrder(t *testing.T) {
	directory := t.TempDir()
	for i := range 20 {
		require.NoError(t, os.WriteFile(filepath.Join(directory, fmt.Sprintf("legacy-%02d.json", i)), nil, 0600))
	}

	var names []string
	require.NoError(t, walkEventFiles(directory, func(_ string, entry os.DirEntry) error {
		names = append(names, entry.Name())
		return nil
	}))

	// Give entries later in the filesystem's actual enumeration an earlier
	// capture time. This exercises selection beyond the first batch regardless
	// of the filesystem's chosen directory order.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range names {
		event := durableTestEvent()
		event.DateTime = base.Add(time.Duration(len(names)-i) * time.Second).Format(time.RFC3339Nano)
		body, err := json.Marshal(event)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), body, 0600))
	}

	d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	batch, err := d.oldestPendingEvents(4)
	require.NoError(t, err)
	require.Len(t, batch, 4)
	for i, record := range batch {
		require.Equal(t, names[len(names)-1-i], record.name)
	}

	retained := directory + "-retained"
	require.NoError(t, os.Rename(directory, retained))
	t.Cleanup(func() { os.Remove(directory); os.Rename(retained, directory) })
	require.NoError(t, os.WriteFile(directory, nil, 0600))
	batch, err = d.oldestPendingEvents(4)
	require.Error(t, err, "a storage scan failure cannot be treated as an empty queue")
	require.Nil(t, batch)
}

func TestDurableReplayLegacyDuplicateRecordsKeepOriginalIdentity(t *testing.T) {
	directory := t.TempDir()
	event := durableTestEvent()
	event.DateTime = "" // Old records can lack ordering metadata.
	body, err := json.Marshal(event)
	require.NoError(t, err)
	for _, name := range []string{"b.json", "a.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), body, 0600))
	}

	d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
	ordered := orderedTestEvent(time.Now().UnixMilli(), 0)
	require.NoError(t, d.Enqueue(context.Background(), ordered))
	batch, err := d.oldestPendingEvents(4)
	require.NoError(t, err)
	require.Len(t, batch, 3)
	require.Equal(t, "a.json", batch[0].name)
	require.Equal(t, "b.json", batch[1].name)
	posted := replayRecorder(t, d)
	d.Start()
	eventuallyEmpty(t, directory)
	d.Close()
	require.Len(t, *posted, 3)
	require.Equal(t, event.EventKey, (*posted)[0].EventKey)
	require.Equal(t, (*posted)[0], (*posted)[1], "duplicate legacy files must reuse identical API identity and wire fields")
	require.Equal(t, ordered.EventKey, (*posted)[2].EventKey)
}

func BenchmarkDurableReplaySelection(b *testing.B) {
	for _, count := range []int{1000, 30000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			directory := b.TempDir()
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
			for i := range count {
				event := orderedTestEvent(base+int64(i), 0)
				if err := os.WriteFile(filepath.Join(directory, event.EventKey+".json"), nil, 0600); err != nil {
					b.Fatal(err)
				}
			}

			d, err := NewDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 0, 0)
			if err != nil {
				b.Fatal(err)
			}

			b.Cleanup(d.Close)
			b.ResetTimer()
			for range b.N {
				batch, err := d.oldestPendingEvents(eventReplayBatchSize)
				if err != nil || len(batch) != min(count, eventReplayBatchSize) {
					b.Fatalf("batch size %d, error %v", len(batch), err)
				}
			}
		})
	}
}
