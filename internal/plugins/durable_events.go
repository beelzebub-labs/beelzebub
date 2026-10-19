package plugins

// The file/fsync/rename protocol and synchronous tracer boundary are adapted
// from beelzebub-cloud/beelzebub-sensor/patches/reliable-events.patch.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

const DefaultEventSpoolMaxBytes int64 = 1 << 30
const DefaultEventSpoolMaxRecords int64 = 100000

var ErrEventSpoolCapacity = errors.New("event spool capacity exhausted")
var ErrEventSpoolClosed = errors.New("event spool closed; event not accepted")
var ErrEventTooLarge = errors.New("event exceeds entire spool byte budget; event not accepted")

var errInvalidSpoolRecord = errors.New("invalid spool record")

type spoolRecord struct {
	Version     int             `json:"version"`
	EventKey    string          `json:"eventKey"`
	Destination string          `json:"destination"`
	Payload     json.RawMessage `json:"payload"`
}

type eventPostResult struct {
	status     int
	key        string
	keyPresent bool
	invalidAck bool
	retryAfter time.Duration
}

// DurableEvents owns one spool. Enqueue success means fsync completed, not API delivery.
// Close never discards pending records and also wakes captures under backpressure.
type DurableEvents struct {
	directory              string
	destination            string
	maxBytes, maxRecords   int64
	usedBytes, usedRecords int64
	usageVersion           uint64
	usageValid             bool
	lock                   *os.File
	mu                     sync.Mutex
	ctx                    context.Context
	cancel                 context.CancelFunc
	done                   chan struct{}
	wake                   chan struct{}
	startOnce, closeOnce   sync.Once
	reconcileOnce          sync.Once
	reconcileDone          chan struct{}
	reconcileWake          chan struct{}
	scanUsage              func() (int64, int64, error)
	post                   func(context.Context, []byte) (eventPostResult, error)
	syncDir                func(string) error
	rename                 func(string, string) error
	remove                 func(string) error
	writeFile              func(string, []byte) error
	retryBase              time.Duration
	retryCap               time.Duration
	slowRetry              time.Duration
}

func NewDurableEvents(uri, token, directory string, maxBytes, maxRecords int64) (*DurableEvents, error) {
	return newDurableEvents(uri, token, directory, maxBytes, maxRecords, syncEventDirectory)
}

func newDurableEvents(uri, token, directory string, maxBytes, maxRecords int64, syncDir func(string) error) (*DurableEvents, error) {
	if maxBytes < 0 || maxRecords < 0 {
		return nil, errors.New("event spool limits must be positive")
	}

	if maxBytes == 0 {
		maxBytes = DefaultEventSpoolMaxBytes
	}

	if maxRecords == 0 {
		maxRecords = DefaultEventSpoolMaxRecords
	}

	if token == "" {
		return nil, errors.New("durable events require a cloud auth token")
	}

	if directory == "" {
		return nil, errors.New("durable events require a spool directory")
	}

	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}

	// Keep operator-provisioned permissions, including root-owned fsGroup
	// mounts. Group write access permits capture but does not permit chmod.
	if err := makeDurableEventDirectory(directory, syncDir); err != nil {
		return nil, fmt.Errorf("prepare event spool directory: %w", err)
	}

	lock, err := os.OpenFile(filepath.Join(directory, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("event spool is already owned by another process")
	}

	ctx, cancel := context.WithCancel(context.Background())
	fingerprint := sha256.Sum256([]byte(strings.TrimRight(uri, "/") + "\x00" + token))
	d := &DurableEvents{directory: directory, destination: hex.EncodeToString(fingerprint[:]), maxBytes: maxBytes, maxRecords: maxRecords, lock: lock, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), syncDir: syncDir, rename: os.Rename, remove: os.Remove, writeFile: writeEventFile, retryBase: time.Second, retryCap: 30 * time.Second, slowRetry: time.Minute}

	d.reconcileDone = make(chan struct{})
	d.reconcileWake = make(chan struct{}, 1)
	d.scanUsage = d.diskUsage
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	d.post = func(ctx context.Context, payload []byte) (eventPostResult, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(uri, "/")+"/events", bytes.NewReader(payload))
		if err != nil {
			return eventPostResult{}, errors.New("invalid event endpoint")
		}

		req.Header.Set("Authorization", token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return eventPostResult{}, errors.New("event POST transport failure or timeout")
		}

		defer resp.Body.Close()
		result := eventPostResult{status: resp.StatusCode, retryAfter: eventRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
		if resp.StatusCode == http.StatusOK {
			// Core can echo the full EventDTO. Allow JSON escaping to expand the
			// posted data by up to six bytes per byte, plus envelope headroom.
			limit := int64(len(payload))*6 + 64*1024
			reader := &io.LimitedReader{R: resp.Body, N: limit + 1}
			result.key, result.keyPresent, err = readEventAcknowledgement(reader)
			result.invalidAck = err != nil || reader.N == 0
		}

		return result, nil
	}

	if err = d.recover(); err != nil {
		d.Close()
		return nil, err
	}

	d.usedBytes, d.usedRecords, err = d.scanUsage()
	if err != nil {
		d.Close()
		return nil, err
	}

	d.usageValid = true
	return d, nil
}

// readEventAcknowledgement walks the response rather than retaining a second
// EventDTO or a map of raw captured fields. A legacy non-object body still
// acknowledges HTTP 200; a structured acknowledgement must be complete.
func readEventAcknowledgement(body io.Reader) (key string, present bool, err error) {
	reader := &acknowledgementReader{Reader: body}
	defer func() {
		if err == nil {
			err = reader.readErr
		}
	}()

	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()

	if token != json.Delim('{') {
		var syntaxErr *json.SyntaxError
		if err != nil && err != io.EOF && !errors.As(err, &syntaxErr) {
			return "", false, err
		}

		_, err = io.Copy(io.Discard, reader)
		return "", false, err
	}

	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return "", present, err
		}

		if name, ok := name.(string); ok && strings.EqualFold(name, "EventKey") {
			value, err := decoder.Token()
			if err != nil {
				return "", true, err
			}

			valueKey, ok := value.(string)
			if !ok || present && key != valueKey {
				return "", true, errors.New("invalid acknowledgement identity")
			}

			key, present = valueKey, true
		} else if err := skipAcknowledgementValue(decoder); err != nil {
			return "", present, err
		}
	}

	if _, err := decoder.Token(); err != nil {
		return "", present, err
	}

	if _, err := decoder.Token(); err != io.EOF {
		return "", present, errors.New("trailing acknowledgement data")
	}

	return key, present, nil
}

// Decoder buffering must not hide a transport error returned alongside bytes
// from a legacy, non-JSON acknowledgement.
type acknowledgementReader struct {
	io.Reader
	readErr error
}

func (r *acknowledgementReader) Read(body []byte) (int, error) {
	n, err := r.Reader.Read(body)
	if err != nil && err != io.EOF {
		r.readErr = err
	}
	return n, err
}

func skipAcknowledgementValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}

	depth := 0
	if token == json.Delim('{') || token == json.Delim('[') {
		depth = 1
	}

	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}

		switch token {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}

	return nil
}

func writeEventFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}

	return errors.Join(err, f.Close())
}

func syncEventDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	defer f.Close()
	return f.Sync()
}

// Persist each entry naming the spool, from the filesystem root down. Sync
// existing entries too: a previous initialization may have created a directory
// and then failed its parent fsync. A retry must not mistake that for durability.
// This runs only during initialization, never on the per-event capture path.
func makeDurableEventDirectory(path string, syncDir func(string) error) error {
	parent := filepath.Dir(path)
	if parent == path {
		return nil
	}

	if err := makeDurableEventDirectory(parent, syncDir); err != nil {
		return err
	}

	if err := os.Mkdir(path, 0700); err != nil {
		if !os.IsExist(err) {
			return err
		}

		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}

		if !info.IsDir() {
			return &os.PathError{Op: "mkdir", Path: path, Err: syscall.ENOTDIR}
		}
	}

	if err := syncDir(parent); err != nil {
		return fmt.Errorf("sync parent directory %q: %w", parent, err)
	}

	return nil
}

func (d *DurableEvents) Start() {
	d.startReconciler()
	d.startOnce.Do(func() { go func() { defer close(d.done); d.run() }() })
}

func (d *DurableEvents) Close() {
	d.closeOnce.Do(func() {
		d.cancel()
		d.Start()
		<-d.done
		<-d.reconcileDone
		// Serialize with a write already in progress before releasing ownership.
		d.mu.Lock()
		defer d.mu.Unlock()
		syscall.Flock(int(d.lock.Fd()), syscall.LOCK_UN)
		d.lock.Close()
	})
}

func (d *DurableEvents) Enqueue(ctx context.Context, event tracer.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if d.ctx.Err() != nil {
		return ErrEventSpoolClosed
	}

	if event.EventKey == "" {
		key, err := uuid.NewV7()

		if err != nil {
			return fmt.Errorf("generate durable event key: %w", err)
		}

		event.EventKey = key.String()
	}

	if _, err := uuid.Parse(event.EventKey); err != nil {
		return errors.New("invalid durable event key")
	}

	if event.DateTime == "" {
		event.DateTime = time.Now().UTC().Format(time.RFC3339Nano)
	}
	dto, err := (&beelzebubCloud{}).mapToEventDTO(event)

	if err != nil {
		return err
	}
	payload, err := json.Marshal(dto)

	if err != nil {
		return err
	}

	record := spoolRecord{Version: 1, EventKey: event.EventKey, Destination: d.destination, Payload: payload}
	body, err := json.Marshal(record)

	if err != nil {
		return err
	}

	if int64(len(body)) > d.maxBytes {
		return ErrEventTooLarge
	}

	warned := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if d.ctx.Err() != nil {
			return ErrEventSpoolClosed
		}

		d.mu.Lock()
		err = d.enqueue(record.EventKey, body)
		d.mu.Unlock()
		if err == nil {
			d.notify()
			return nil
		}

		if errors.Is(err, ErrEventSpoolClosed) {
			return err
		}

		if errors.Is(err, ErrEventSpoolCapacity) {
			d.requestReconciliation()
		}

		if !warned {
			log.WithField("event_key", event.EventKey).Error("Event spool unavailable or full; capture blocked until storage recovers")
			warned = true
		}

		if !d.wait(ctx, d.retryBase) {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			return ErrEventSpoolClosed
		}
	}
}

func (d *DurableEvents) enqueue(key string, body []byte) error {
	if d.ctx.Err() != nil {
		return ErrEventSpoolClosed
	}

	path := filepath.Join(d.directory, key+".json")
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, body) {
			return errors.New("event key conflicts with spool record")
		}

		return d.syncDir(d.directory)
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary := filepath.Join(d.directory, ".event-"+key+".tmp")
	if !d.usageValid {
		return ErrEventSpoolCapacity
	}

	var previous int64
	var count int64 = 1
	if info, err := os.Stat(temporary); err == nil {
		previous = info.Size()
		count = 0
	} else if !os.IsNotExist(err) {
		return err
	}

	if d.usedBytes-previous+int64(len(body)) > d.maxBytes || d.usedRecords+count > d.maxRecords {
		return ErrEventSpoolCapacity
	}

	// Account for partial writes as well as complete records. Only this process
	// mutates spool files; a failed stat suspends admission until reconciliation.
	writeErr := d.writeFile(temporary, body)
	d.usageVersion++
	info, statErr := os.Stat(temporary)
	if statErr == nil {
		d.usedBytes += info.Size() - previous
		d.usedRecords += count
	} else if os.IsNotExist(statErr) {
		d.usedBytes -= previous
		d.usedRecords -= 1 - count
	} else {
		d.usageValid = false
		d.requestReconciliation()
	}

	if writeErr != nil {
		return writeErr
	}

	if statErr != nil {
		return statErr
	}

	if err := d.rename(temporary, path); err != nil {
		return err
	}

	return d.syncDir(d.directory)
}

func (d *DurableEvents) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *DurableEvents) wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-d.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// walk reads fixed-size batches so a large retained spool does not grow memory.
func walkEventFiles(directory string, visit func(string, os.DirEntry) error) error {
	f, err := os.Open(directory)
	if err != nil {
		return err
	}

	defer f.Close()
	for {
		entries, err := f.ReadDir(128)
		for _, entry := range entries {
			if entry.Type().IsRegular() && entry.Name() != ".lock" {
				if err := visit(filepath.Join(directory, entry.Name()), entry); err != nil {
					return err
				}
			}
		}

		if err == io.EOF {
			return nil
		}

		if err != nil {
			return err
		}
	}
}

func (d *DurableEvents) diskUsage() (int64, int64, error) {
	var size, count int64
	visit := func(_ string, e os.DirEntry) error {
		if err := d.ctx.Err(); err != nil {
			return err
		}

		info, err := e.Info()
		if os.IsNotExist(err) {
			return nil
		}

		if err != nil {
			return err
		}

		size += info.Size()
		count++
		return nil
	}

	if err := walkEventFiles(d.directory, visit); err != nil {
		return 0, 0, err
	}

	if err := walkEventFiles(filepath.Join(d.directory, "quarantine"), visit); err != nil && !os.IsNotExist(err) {
		return 0, 0, err
	}

	return size, count, nil
}

func (d *DurableEvents) startReconciler() {
	d.reconcileOnce.Do(func() {
		go func() {
			defer close(d.reconcileDone)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()

			for {
				select {
				case <-d.ctx.Done():
					return
				case <-ticker.C:
				case <-d.reconcileWake:
				}

				d.reconcileUsage()
				// Coalesce capacity pressure; new captures cannot cause repeated scans.
				if !d.wait(d.ctx, time.Second) {
					return
				}
			}
		}()
	})
}

func (d *DurableEvents) requestReconciliation() {
	d.startReconciler()
	select {
	case d.reconcileWake <- struct{}{}:
	default:
	}
}

func (d *DurableEvents) reconcileUsage() {
	d.mu.Lock()
	version := d.usageVersion
	d.mu.Unlock()
	// Filesystem enumeration never holds the capture/delivery mutex. A scan
	// racing a mutation cannot replace newer accounting with a stale snapshot.
	size, count, err := d.scanUsage()
	if err != nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if version != d.usageVersion {
		d.requestReconciliation()
		return
	}

	d.usedBytes, d.usedRecords, d.usageValid = size, count, true
}

func (d *DurableEvents) readRecord(path string) (record spoolRecord, recordErr error) {
	f, err := os.Open(path)
	if err != nil {
		return spoolRecord{}, err
	}

	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return spoolRecord{}, err
	}

	if info.Size() > d.maxBytes {
		return spoolRecord{}, errors.New("record exceeds spool budget")
	}

	defer func() {
		var pathErr *os.PathError
		if recordErr != nil && !errors.As(recordErr, &pathErr) {
			recordErr = fmt.Errorf("%w: %v", errInvalidSpoolRecord, recordErr)
		}
	}()

	decoder := json.NewDecoder(io.LimitReader(f, d.maxBytes))
	var raw json.RawMessage
	if err = decoder.Decode(&raw); err != nil {
		return record, err
	}

	if err = decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			err = errors.New("trailing spool data")
		}

		return record, err
	}

	if err = json.Unmarshal(raw, &record); err != nil {
		return record, err
	}

	if record.Version == 0 {
		// Reference patch records are raw tracer events. Convert once on replay,
		// retaining the original key and time rather than capturing a new event.
		var event tracer.Event

		if err = json.Unmarshal(raw, &event); err != nil {
			return record, err
		}

		if event.EventKey == "" {
			return record, errors.New("missing event key")
		}

		// The pinned reference patch's DTO omitted these fields even when the
		// raw tracer record contained CommandRaw. Replaying them now changes the
		// API's acceptance fingerprint after an ambiguous acknowledgement.
		event.CommandRaw = ""
		event.Metadata = nil
		dto, err := (&beelzebubCloud{}).mapToEventDTO(event)
		if err != nil {
			return record, err
		}

		record.Payload, err = json.Marshal(dto)
		if err != nil {
			return record, err
		}

		record.EventKey = event.EventKey
	} else if record.Version != 1 {
		return record, errors.New("unsupported spool version")
	}

	var dto EventDTO
	if err = json.Unmarshal(record.Payload, &dto); err != nil || dto.EventKey == "" || dto.EventKey != record.EventKey {
		return record, errors.New("invalid spool payload identity")
	}

	if strings.ContainsAny(record.EventKey, "/\\") {
		return record, errors.New("invalid spool key")
	}

	return record, nil
}

func (d *DurableEvents) quarantine(path, reason string) error {
	directory := filepath.Join(d.directory, "quarantine")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}

	target := filepath.Join(directory, filepath.Base(path)+"."+reason+"."+uuid.NewString())
	if err := d.rename(path, target); err != nil {
		d.usageVersion++
		return err
	}

	d.usageVersion++
	if err := d.syncDir(directory); err != nil {
		return err
	}

	if err := d.syncDir(d.directory); err != nil {
		return err
	}

	log.WithField("reason", reason).Error("Event record retained in spool quarantine; operator action required")
	return nil
}

func (d *DurableEvents) recover() error {
	return walkEventFiles(d.directory, func(path string, entry os.DirEntry) error {
		if !strings.HasPrefix(entry.Name(), ".event-") || !strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}

		record, err := d.readRecord(path)
		if err != nil {
			if errors.Is(err, errInvalidSpoolRecord) {
				return d.quarantine(path, "corrupt")
			}
			return err
		}

		// A complete temporary write may have crashed before file fsync.
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}

		err = errors.Join(f.Sync(), f.Close())
		if err != nil {
			return err
		}

		target := filepath.Join(d.directory, record.EventKey+".json")
		if existing, err := d.readRecord(target); err == nil {
			if existing.EventKey != record.EventKey || !bytes.Equal(existing.Payload, record.Payload) {
				return d.quarantine(path, "conflict")
			}
			if err := d.remove(path); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			if errors.Is(err, errInvalidSpoolRecord) {
				return d.quarantine(path, "conflict")
			}
			return err
		} else if err := d.rename(path, target); err != nil {
			return err
		}

		return d.syncDir(d.directory)
	})
}

func eventRetryAfter(value string, now time.Time) time.Duration {
	var delay time.Duration
	if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if n > 900 {
			return 15 * time.Minute
		}
		if n > 0 {
			delay = time.Duration(n) * time.Second
		}
	} else if date, err := http.ParseTime(value); err == nil {
		delay = date.Sub(now)
	}

	return max(0, min(delay, 15*time.Minute))
}

func (d *DurableEvents) deliver(path string) (time.Duration, bool) {
	d.mu.Lock()
	record, err := d.readRecord(path)
	if os.IsNotExist(err) {
		d.mu.Unlock()
		return 0, true
	}

	if err != nil {
		if errors.Is(err, errInvalidSpoolRecord) {
			err = d.quarantine(path, "corrupt")
		} else {
			log.Error("Cannot read pending event; check storage access and spool byte budget")
		}

		d.mu.Unlock()
		return d.slowRetry, err == nil
	}

	// Also makes a rename whose earlier directory fsync failed durable before POST.
	err = d.syncDir(d.directory)
	d.mu.Unlock()
	if err != nil {
		return 0, false
	}

	if record.Destination != "" && record.Destination != d.destination {
		log.Error("Pending event destination or token changed; restore original settings before replay")
		return d.slowRetry, false
	}

	result, err := d.post(d.ctx, record.Payload)
	if err == nil && result.status == http.StatusOK && !result.invalidAck && (!result.keyPresent || result.key == record.EventKey) {
		d.mu.Lock()
		err = d.removeRecord(path)

		if err == nil {
			err = d.syncDir(d.directory)
		}

		d.mu.Unlock()
		return 0, err == nil
	}

	if err == nil {
		switch result.status {
		case 400, 409, 413, 422:
			d.mu.Lock()
			err = d.quarantine(path, fmt.Sprintf("http-%d", result.status))
			d.mu.Unlock()
			return d.slowRetry, err == nil
		}
	}

	log.WithFields(log.Fields{"event_key": record.EventKey, "http_status": result.status}).Warn("Event POST unacknowledged; original event remains queued")
	if err != nil || result.status == 408 || result.status == 429 || result.status >= 500 && result.status <= 599 {
		return result.retryAfter, false
	}

	return max(d.slowRetry, result.retryAfter), false
}

// removeRecord runs under mu and releases capacity as soon as the unlink
// succeeds, even if syncing its directory subsequently fails.
func (d *DurableEvents) removeRecord(path string) error {
	info, statErr := os.Stat(path)
	err := d.remove(path)
	d.usageVersion++
	_, afterErr := os.Stat(path)

	if os.IsNotExist(afterErr) && statErr == nil {
		d.usedBytes -= info.Size()
		d.usedRecords--
	} else if statErr != nil || afterErr != nil {
		d.usageValid = false
		d.requestReconciliation()
	}

	return err
}

func (d *DurableEvents) run() {
	retry := d.retryBase
	var pending []eventReplayEntry
	for d.ctx.Err() == nil {
		var err error
		if len(pending) == 0 {
			pending, err = d.oldestPendingEvents(eventReplayBatchSize)
			if err != nil {
				log.Error("Unable to scan event spool; records retained")
			}
		}

		if err == nil && len(pending) == 0 {
			select {
			case <-d.ctx.Done():
				return
			case <-d.wake:
			case <-time.After(d.retryBase):
			}
			continue
		}

		var requested time.Duration
		if err == nil {
			var complete bool
			requested, complete = d.deliver(filepath.Join(d.directory, pending[0].name))
			if complete {
				// Keep only filenames/order metadata, never queued event payloads.
				pending[0] = eventReplayEntry{}
				pending = pending[1:]
				retry = d.retryBase
				continue
			}
		}

		// Retry the same head before advancing. Retain the batch during an
		// outage rather than repeatedly scanning/sorting the whole backlog.
		// Enqueue never wakes or shortens this retry delay.
		delay := retry/2 + time.Duration(rand.Int64N(max(1, int64(retry/2))))
		if !d.wait(d.ctx, max(delay, requested)) {
			return
		}
		retry = min(retry*2, d.retryCap)
	}
}
