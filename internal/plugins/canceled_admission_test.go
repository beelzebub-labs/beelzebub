package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/google/uuid"
)

func TestDurableCanceledAdmissionAndStorageFailure(t *testing.T) {
	for _, mode := range []string{"healthy", "full", "unavailable", "closed", "write-error"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "spool")
			capacity := int64(10)
			if mode == "full" {
				capacity = 1
			}

			d, err := NewDurableEvents("http://synthetic.invalid", "synthetic-token", directory, 1<<20, capacity)
			if err != nil {
				t.Fatal(err)
			}

			defer d.Close()
			event := tracer.Event{EventKey: uuid.NewString(), ID: "synthetic-canceled", Protocol: "HTTP", Body: "user=admin&pass=123456"}
			if mode == "full" {
				seed := event
				seed.EventKey = uuid.NewString()
				if err := d.Enqueue(context.Background(), seed); err != nil {
					t.Fatal(err)
				}
			}

			if mode == "unavailable" {
				if err := os.Rename(directory, directory+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(directory, []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			if mode == "closed" {
				d.Close()
			}

			var attempts atomic.Int64
			if mode == "write-error" {
				d.writeFile = func(string, []byte) error { attempts.Add(1); return errors.New("synthetic disk-full error") }
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			start := time.Now()
			err = d.Enqueue(ctx, event)
			elapsed := time.Since(start)
			if mode != "healthy" && elapsed > time.Second {
				t.Errorf("canceled admission retained producer for %s", elapsed)
			}

			switch mode {
			case "healthy":
				if err != nil {
					t.Errorf("healthy admission failed: %v", err)
				}

				paths, _ := filepath.Glob(filepath.Join(directory, "*.json"))
				if len(paths) != 1 {
					t.Errorf("healthy spool persisted %d events, want 1", len(paths))
				}
			case "closed":
				if !errors.Is(err, ErrEventSpoolClosed) {
					t.Errorf("shutdown ignored: %v", err)
				}
			case "write-error":
				if !errors.Is(err, context.Canceled) {
					t.Errorf("storage failure did not release canceled producer: %v", err)
				}

				if attempts.Load() != 1 {
					t.Errorf("expected exactly one write attempt, got %d", attempts.Load())
				}
			default:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("unavailable/full spool did not cancel: %v", err)
				}
			}

			t.Logf("canceled admission mode=%s elapsed=%s error=%v writes=%d", mode, elapsed, err, attempts.Load())
		})
	}
}
