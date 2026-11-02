package historystore

import (
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
)

func TestQueryReturnsCopyNotBackingArray(t *testing.T) {
	hs := NewHistoryStore()
	// Give the baseline implementation a deliberately spare backing slot. This
	// makes the aliasing check independent of Go's slice-growth heuristics.
	seed := make([]plugins.Message, 3, 4)
	for i, c := range []string{"1", "2", "3"} {
		seed[i] = plugins.Message{Role: "user", Content: c}
	}
	hs.sessions["k"] = HistoryEvent{Messages: seed}
	got := hs.Query("k")
	local := append(got, plugins.Message{Role: "user", Content: "LOCAL"})
	hs.Append("k", plugins.Message{Role: "user", Content: "STORE"})

	if local[3].Content != "LOCAL" {
		t.Fatalf("caller's local turn overwritten: got %q", local[3].Content)
	}
	if hs.Query("k")[0].Content != "1" {
		t.Fatal("Query result aliases store contents")
	}
}

func TestQuerySnapshotsRemainIndependentAcrossConcurrentAppends(t *testing.T) {
	hs := NewHistoryStore()
	seed := make([]plugins.Message, 3, 4)
	for i, c := range []string{"1", "2", "3"} {
		seed[i] = plugins.Message{Role: "user", Content: c}
	}
	hs.sessions["k"] = HistoryEvent{Messages: seed}

	const rounds = 16
	snapshots := make([][]plugins.Message, rounds)
	queried := make(chan int)
	checked := make([]chan struct{}, rounds)
	appended := make(chan struct{})
	done := make(chan struct{})
	for i := range checked {
		checked[i] = make(chan struct{})
	}
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			snapshots[i] = append(hs.Query("k"), plugins.Message{
				Role:    "user",
				Content: "LOCAL",
			})
			queried <- i
			<-checked[i]
			hs.Append("k", plugins.Message{Role: "assistant", Content: "STORE"})
			appended <- struct{}{}
		}
	}()

	for i := 0; i < rounds; i++ {
		if got := <-queried; got != i {
			t.Fatalf("query round = %d, want %d", got, i)
		}
		close(checked[i])
		<-appended
		if got := snapshots[i][3+i].Content; got != "LOCAL" {
			t.Fatalf("caller %d snapshot overwritten: got %q", i, got)
		}
	}
	<-done

	store := hs.Query("k")
	if len(store) != 3+rounds {
		t.Fatalf("store length = %d, want %d", len(store), 3+rounds)
	}
	for i, snapshot := range snapshots {
		if got := snapshot[3+i].Content; got != "LOCAL" {
			t.Fatalf("caller %d snapshot overwritten: got %q", i, got)
		}
	}
}
