package plugins

import (
	"errors"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

// These limits bound captures before durable acceptance, independently of the
// disk spool's byte/record limits. Reservation acquisition never waits.
const DurableCaptureMaxRecords int64 = 256
const DurableCaptureMaxBytes int64 = 64 << 20

var ErrDurableCaptureCapacity = errors.New("durable capture memory or concurrency budget exhausted; event not accepted")

// admissionSize conservatively charges the source event, DTO/envelope encoding,
// JSON escaping and encoder scratch space. This is an admission estimate, not a
// reservation of Go heap or an OS memory limit. Saturation avoids overflow and
// rejects very large events without copying or encoding their contents.
func admissionSize(event tracer.Event) int64 {
	size := int64(4096)
	add := func(value string, multiplier int64) {
		// Include entry/slice overhead even for empty strings in metadata.
		if size > DurableCaptureMaxBytes-64 || int64(len(value)) > (DurableCaptureMaxBytes-size-64)/multiplier {
			size = DurableCaptureMaxBytes + 1
		} else {
			size += 64 + int64(len(value))*multiplier
		}
	}

	for _, value := range [...]string{
		event.EventKey, event.DateTime, event.RemoteAddr, event.Protocol,
		event.Command, event.CommandRaw, event.CommandOutput, event.Status,
		event.Msg, event.ID, event.Environ, event.User, event.Password,
		event.Client, event.Cookies, event.UserAgent, event.HostHTTPRequest,
		event.Body, event.HTTPMethod, event.RequestURI, event.Description,
		event.SourceIp, event.SourcePort, event.TLSServerName, event.Handler,
	} {
		add(value, 16)
	}

	// Headers is itself JSON-encoded before it becomes an EventDTO string.
	add(event.Headers, 64)
	for key, values := range event.HeadersMap {
		add(key, 16)
		for _, value := range values {
			add(value, 16)
			if size > DurableCaptureMaxBytes {
				return size
			}
		}

		if size > DurableCaptureMaxBytes {
			return size
		}
	}

	for key, value := range event.Metadata {
		add(key, 16)
		add(value, 16)
		if size > DurableCaptureMaxBytes {
			return size
		}
	}

	return size
}

func (d *DurableEvents) reserveAdmission(event tracer.Event) (int64, error) {
	if err := d.memoryCheck(); err != nil {
		return 0, err
	}

	size := admissionSize(event)
	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	if d.ctx.Err() != nil {
		return 0, ErrEventSpoolClosed
	}

	if d.admissionRecords >= DurableCaptureMaxRecords || size > DurableCaptureMaxBytes-d.admissionBytes {
		return 0, ErrDurableCaptureCapacity
	}

	d.admissionRecords++
	d.admissionBytes += size
	return size, nil
}

func (d *DurableEvents) releaseAdmission(size int64) {
	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	d.admissionRecords--
	d.admissionBytes -= size
}
