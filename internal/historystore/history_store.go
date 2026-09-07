package historystore

import (
	"sync"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
)

var (
	MaxHistoryAge   = 60 * time.Minute
	CleanerInterval = 1 * time.Minute
)

// HistoryStore is a thread-safe structure for storing Messages used to build LLM Context.
type HistoryStore struct {
	sync.RWMutex
	sessions map[string]HistoryEvent
	done     chan struct{}
}

// HistoryEvent is a container for storing messages
type HistoryEvent struct {
	LastSeen time.Time
	Messages []plugins.Message
}

// NewHistoryStore returns a prepared HistoryStore
func NewHistoryStore() *HistoryStore {
	return &HistoryStore{
		sessions: make(map[string]HistoryEvent),
		done:     make(chan struct{}),
	}
}

// HasKey returns true if the supplied key exists in the map.
func (hs *HistoryStore) HasKey(key string) bool {
	hs.RLock()
	defer hs.RUnlock()
	_, ok := hs.sessions[key]
	return ok
}

// Query returns the value stored at the map
func (hs *HistoryStore) Query(key string) []plugins.Message {
	hs.RLock()
	defer hs.RUnlock()
	return hs.sessions[key].Messages
}

// Append will add the slice of Mesages to the entry for the key.
// If the map has not yet been initalised, then a new map is created.
func (hs *HistoryStore) Append(key string, message ...plugins.Message) {
	hs.Lock()
	defer hs.Unlock()
	// In the unexpected case that the map has not yet been initalised, create it.
	if hs.sessions == nil {
		hs.sessions = make(map[string]HistoryEvent)
	}
	e, ok := hs.sessions[key]
	if !ok {
		e = HistoryEvent{}
	}
	e.LastSeen = time.Now()
	e.Messages = append(e.Messages, message...)
	hs.sessions[key] = e
}

// AppendBounded appends messages and retains at most max entries for key.
// A non-positive max preserves the unbounded behavior of Append.
func (hs *HistoryStore) AppendBounded(key string, max int, message ...plugins.Message) {
	hs.Lock()
	defer hs.Unlock()
	// In the unexpected case that the map has not yet been initalised, create it.
	if hs.sessions == nil {
		hs.sessions = make(map[string]HistoryEvent)
	}
	e, ok := hs.sessions[key]
	if !ok {
		e = HistoryEvent{}
	}
	e.LastSeen = time.Now()
	e.Messages = append(e.Messages, message...)
	if max > 0 && len(e.Messages) > max {
		trimmed := make([]plugins.Message, max)
		copy(trimmed, e.Messages[len(e.Messages)-max:])
		e.Messages = trimmed
	}
	hs.sessions[key] = e
}

// HistoryCleaner starts a background goroutine that periodically removes records
// from the HistoryStore that are older than MaxHistoryAge. Call Close to stop it.
func (hs *HistoryStore) HistoryCleaner() {
	cleanerTicker := time.NewTicker(CleanerInterval)
	go func() {
		defer cleanerTicker.Stop()
		for {
			select {
			case <-hs.done:
				return
			case <-cleanerTicker.C:
				hs.Lock()
				for k, v := range hs.sessions {
					if time.Since(v.LastSeen) > MaxHistoryAge {
						delete(hs.sessions, k)
					}
				}
				hs.Unlock()
			}
		}
	}()
}

// Close stops the HistoryCleaner goroutine. It is safe to call multiple times.
func (hs *HistoryStore) Close() {
	select {
	case <-hs.done:
	default:
		close(hs.done)
	}
}
