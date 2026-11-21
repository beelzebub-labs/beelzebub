package plugins

import (
	"cmp"
	"container/heap"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Bound replay memory independently of configured capacity and retained files.
// A full batch is drained before another scan; capture never builds this index.
const eventReplayBatchSize = 16384

type eventReplayEntry struct {
	name  string
	key   string
	milli int64
	known bool
}

func compareEventReplay(a, b eventReplayEntry) int {
	// Records without usable ordering metadata go first, deterministically.
	// Delivery still validates them and preserves corrupt data in quarantine.
	if a.known != b.known {
		if !a.known {
			return -1
		}
		return 1
	}

	if order := cmp.Compare(a.milli, b.milli); order != 0 {
		return order
	}

	if order := strings.Compare(a.key, b.key); order != 0 {
		return order
	}

	return strings.Compare(a.name, b.name)
}

// A max-heap retains only the oldest batch while walking an unsorted directory.
type eventReplayHeap []eventReplayEntry

func (h eventReplayHeap) Len() int           { return len(h) }
func (h eventReplayHeap) Less(i, j int) bool { return compareEventReplay(h[i], h[j]) > 0 }
func (h eventReplayHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *eventReplayHeap) Push(value any)    { *h = append(*h, value.(eventReplayEntry)) }
func (h *eventReplayHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	(*h)[last] = eventReplayEntry{}
	*h = (*h)[:last]
	return value
}

func (d *DurableEvents) oldestPendingEvents(limit int) ([]eventReplayEntry, error) {
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}

	var pending eventReplayHeap
	err := walkEventFiles(d.directory, func(path string, entry os.DirEntry) error {
		if err := d.ctx.Err(); err != nil {
			return err
		}

		if !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}

		candidate := d.eventReplayOrder(path, entry.Name())
		if len(pending) < limit {
			heap.Push(&pending, candidate)
		} else if compareEventReplay(candidate, pending[0]) < 0 {
			pending[0] = candidate
			heap.Fix(&pending, 0)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	slices.SortFunc(pending, compareEventReplay)
	return []eventReplayEntry(pending), nil
}

func (d *DurableEvents) eventReplayOrder(path, name string) eventReplayEntry {
	entry := eventReplayEntry{name: name, key: name}
	key, err := uuid.Parse(strings.TrimSuffix(name, ".json"))
	if err == nil && key.Version() == 7 {
		seconds, nanos := key.Time().UnixTime()
		entry.milli, entry.key, entry.known = seconds*1000+nanos/1e6, key.String(), true
		return entry
	}

	// Older v4 envelopes and raw cloud-patch records keep their identities and
	// use their original capture time. Only these records need payload reads.
	record, err := d.readRecord(path)
	if err != nil {
		return entry
	}

	entry.key = record.EventKey
	if key, err := uuid.Parse(entry.key); err == nil {
		entry.key = key.String()
	}

	var event struct{ DateTime string }
	if json.Unmarshal(record.Payload, &event) == nil {
		if captured, err := time.Parse(time.RFC3339Nano, event.DateTime); err == nil {
			entry.milli, entry.known = captured.UnixMilli(), true
		}
	}

	return entry
}
