package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDurableStartupRejectsInvalidSettingsAndReleasesFailedOwnership(t *testing.T) {
	for _, scenario := range []string{"negative bytes", "negative records", "missing token", "missing directory", "lock directory", "recovery sync failure", "invalid quarantine"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			token := "synthetic-token"
			var maxBytes, maxRecords int64
			syncDir := syncEventDirectory

			switch scenario {
			case "negative bytes":
				maxBytes = -1
			case "negative records":
				maxRecords = -1
			case "missing token":
				token = ""
			case "missing directory":
				directory = ""
			case "lock directory":
				require.NoError(t, os.Mkdir(filepath.Join(directory, ".lock"), 0700))
			case "invalid quarantine":
				require.NoError(t, os.WriteFile(filepath.Join(directory, "quarantine"), nil, 0600))
			case "recovery sync failure":
				d := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
				event := durableTestEvent()
				require.NoError(t, d.Enqueue(context.Background(), event))
				d.Close()
				require.NoError(t, os.Rename(filepath.Join(directory, event.EventKey+".json"), filepath.Join(directory, ".event-"+event.EventKey+".tmp")))
				syncDir = func(path string) error {
					if path == directory {
						return syscall.EIO
					}
					return syncEventDirectory(path)
				}
			}

			d, err := newDurableEvents("http://synthetic.invalid", token, directory, maxBytes, maxRecords, syncDir)
			require.Error(t, err)
			require.Nil(t, d, "failed initialization must not expose an accepting pipeline")

			if scenario == "invalid quarantine" {
				require.NoError(t, os.Remove(filepath.Join(directory, "quarantine")))
			}

			if scenario == "invalid quarantine" || scenario == "recovery sync failure" {
				reopened := testDelivery(t, "http://synthetic.invalid", directory, 0, 0)
				require.NotNil(t, reopened, "failed initialization must release its flock")
			}
		})
	}
}

func TestDurableCaptureValidationAndDuplicateIdentity(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	event := durableTestEvent()
	// Canceled producers can attempt admission when the spool is healthy.
	invalid := event
	invalid.EventKey = "../../invalid"
	require.ErrorContains(t, d.Enqueue(context.Background(), invalid), "invalid durable event key")
	require.Empty(t, pendingFiles(t, d.directory))
	event.DateTime = ""
	require.NoError(t, d.Enqueue(context.Background(), event))
	path := pendingFiles(t, d.directory)[0]
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	var record spoolRecord
	require.NoError(t, json.Unmarshal(original, &record))
	var payload EventDTO
	require.NoError(t, json.Unmarshal(record.Payload, &payload))
	_, err = time.Parse(time.RFC3339Nano, payload.DateTime)
	require.NoError(t, err)
	event.DateTime = payload.DateTime
	require.NoError(t, d.Enqueue(context.Background(), event))
	assertSpoolAccounting(t, d)
	require.Equal(t, int64(1), d.usedRecords)
	event.Command = "conflicting payload"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, d.Enqueue(ctx, event), context.DeadlineExceeded)
	retained, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, retained)
	d.Close()
	require.ErrorIs(t, d.Enqueue(context.Background(), event), ErrEventSpoolClosed)
}

func TestDurableMalformedRecordsArePreservedWithoutPosting(t *testing.T) {
	for name, body := range map[string]string{
		"trailing object":     `{}` + `{}`,
		"trailing syntax":     `{}invalid`,
		"non-object":          `[]`,
		"missing legacy key":  `{}`,
		"invalid legacy type": `{"EventKey":"key","ID":42}`,
		"unsupported version": `{"version":2}`,
		"mismatched identity": `{"version":1,"eventKey":"a","payload":{"EventKey":"b"}}`,
		"invalid payload":     `{"version":1,"eventKey":"a","payload":[]}`,
		"unsafe key":          `{"version":1,"eventKey":"../a","payload":{"EventKey":"../a"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
			path := filepath.Join(d.directory, "bad.json")
			require.NoError(t, os.WriteFile(path, []byte(body), 0600))
			d.post = func(context.Context, []byte) (eventPostResult, error) {
				t.Error("malformed record posted")
				return eventPostResult{}, nil
			}

			_, complete := d.deliver(path)
			require.True(t, complete, "quarantine must allow later records to proceed")
			require.NoFileExists(t, path)
			paths, err := filepath.Glob(filepath.Join(d.directory, "quarantine", "bad.json.corrupt.*"))
			require.NoError(t, err)
			require.Len(t, paths, 1)
			retained, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			require.Equal(t, body, string(retained))
		})
	}
}

func TestDurableOversizedAndUnavailableRecordsRemainPending(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 128, 0)
	path := filepath.Join(d.directory, "large.json")
	body := []byte(strings.Repeat("x", 129))
	require.NoError(t, os.WriteFile(path, body, 0600))
	d.post = func(context.Context, []byte) (eventPostResult, error) {
		t.Error("unreadable record posted")
		return eventPostResult{}, nil
	}

	_, complete := d.deliver(path)
	require.False(t, complete)
	retained, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, retained)
	require.NoDirExists(t, filepath.Join(d.directory, "quarantine"))
	_, complete = d.deliver(filepath.Join(d.directory, "already-removed.json"))
	require.True(t, complete, "a record already removed by an operator cannot block replay")
}

func TestDurableTemporaryRecoveryConflictsAndFailures(t *testing.T) {
	for _, scenario := range []string{"identical", "conflicting", "corrupt target", "unreadable target", "remove failure", "rename failure"} {
		t.Run(scenario, func(t *testing.T) {
			d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
			event := durableTestEvent()
			require.NoError(t, d.Enqueue(context.Background(), event))
			path := filepath.Join(d.directory, event.EventKey+".json")
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			temporary := filepath.Join(d.directory, ".event-"+event.EventKey+".tmp")
			require.NoError(t, os.WriteFile(temporary, body, 0600))

			switch scenario {
			case "conflicting":
				var record spoolRecord
				require.NoError(t, json.Unmarshal(body, &record))
				var payload EventDTO
				require.NoError(t, json.Unmarshal(record.Payload, &payload))
				payload.Command = "different command"
				record.Payload, err = json.Marshal(payload)
				require.NoError(t, err)
				changed, err := json.Marshal(record)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(temporary, changed, 0600))
			case "corrupt target":
				require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0600))
			case "unreadable target":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0700))
			case "remove failure":
				d.remove = func(string) error { return syscall.EIO }
			case "rename failure":
				require.NoError(t, os.Remove(path))
				d.rename = func(string, string) error { return syscall.EIO }
			}

			err = d.recover()
			if strings.HasSuffix(scenario, "failure") || scenario == "unreadable target" {
				require.Error(t, err)
				require.FileExists(t, temporary, "a failed recovery must retain its source")
			} else {
				require.NoError(t, err)
				require.NoFileExists(t, temporary)
				if scenario == "identical" {
					retained, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, body, retained)
				} else {
					paths, err := filepath.Glob(filepath.Join(d.directory, "quarantine", "*.conflict.*"))
					require.NoError(t, err)
					require.Len(t, paths, 1)
				}
			}
		})
	}
}

func TestDurableQuarantineStorageFailuresPreserveEvidence(t *testing.T) {
	for _, scenario := range []string{"directory", "rename", "target sync", "source sync"} {
		t.Run(scenario, func(t *testing.T) {
			d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
			event := durableTestEvent()
			require.NoError(t, d.Enqueue(context.Background(), event))
			path := filepath.Join(d.directory, event.EventKey+".json")
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			d.post = func(context.Context, []byte) (eventPostResult, error) { return eventPostResult{status: 422}, nil }

			switch scenario {
			case "directory":
				require.NoError(t, os.WriteFile(filepath.Join(d.directory, "quarantine"), nil, 0600))
			case "rename":
				d.rename = func(string, string) error { return syscall.EIO }
			case "target sync", "source sync":
				d.syncDir = func(path string) error {
					if filepath.Base(path) == "quarantine" && scenario == "target sync" {
						return syscall.EIO
					}

					if path == d.directory && scenario == "source sync" {
						paths, _ := filepath.Glob(filepath.Join(d.directory, "quarantine", "*"))

						if len(paths) > 0 {
							return syscall.EIO
						}
					}
					return syncEventDirectory(path)
				}
			}

			_, complete := d.deliver(path)
			require.False(t, complete, "failed quarantine is not successful disposition")
			if scenario == "directory" || scenario == "rename" {
				retained, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, body, retained)
			} else {
				paths, err := filepath.Glob(filepath.Join(d.directory, "quarantine", "*"))
				require.NoError(t, err)
				require.Len(t, paths, 1)
				retained, err := os.ReadFile(paths[0])
				require.NoError(t, err)
				require.Equal(t, body, retained)
			}
		})
	}
}

func TestDurableDeliveryRequiresDirectorySyncBeforePosting(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	d.syncDir = func(string) error { return syscall.EIO }
	d.post = func(context.Context, []byte) (eventPostResult, error) {
		t.Error("posted before directory was synced")
		return eventPostResult{}, nil
	}

	_, complete := d.deliver(pendingFiles(t, d.directory)[0])
	require.False(t, complete)
	require.Len(t, pendingFiles(t, d.directory), 1)
}

func TestDurableFailedReconciliationKeepsLastAccounting(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	bytes, records := d.usedBytes, d.usedRecords
	d.scanUsage = func() (int64, int64, error) { return 0, 0, syscall.EIO }
	d.reconcileUsage()
	require.Equal(t, bytes, d.usedBytes)
	require.Equal(t, records, d.usedRecords)
	d.cancel()
	_, _, err := d.diskUsage()
	require.ErrorIs(t, err, context.Canceled)
}

func TestDurableInvalidEndpointDoesNotDiscardRecord(t *testing.T) {
	d := testDelivery(t, "http://[invalid", t.TempDir(), 0, 0)
	require.NoError(t, d.Enqueue(context.Background(), durableTestEvent()))
	_, complete := d.deliver(pendingFiles(t, d.directory)[0])
	require.False(t, complete)
	require.Len(t, pendingFiles(t, d.directory), 1)
}

func TestDurableShortWriteFailureKeepsAccurateCapacity(t *testing.T) {
	d := testDelivery(t, "http://synthetic.invalid", t.TempDir(), 0, 0)
	d.writeFile = func(string, []byte) error { return syscall.ENOSPC }
	err := d.enqueue(durableTestEvent().EventKey, []byte("unwritten"))
	require.ErrorIs(t, err, syscall.ENOSPC)
	assertSpoolAccounting(t, d)
	require.Zero(t, d.usedRecords)
	require.Zero(t, d.usedBytes)
	require.Error(t, writeEventFile(filepath.Join(d.directory, "missing", "file"), nil))
	require.True(t, errors.Is(syncEventDirectory(filepath.Join(d.directory, "missing")), os.ErrNotExist))
}
