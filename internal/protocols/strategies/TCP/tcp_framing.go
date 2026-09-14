package TCP

import (
	"errors"
	"net"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
)

// accumulationGrace is how long the opportunistic reader waits for more bytes
// when only a catch-all handler would match the data so far. Short enough not
// to add noticeable latency, long enough to reassemble a split binary message.
const accumulationGrace = 150 * time.Millisecond

// maxFrameSize bounds a single application frame to avoid huge allocations
// driven by malicious or malformed framing metadata.
const maxFrameSize = 1 << 20 // 1 MiB

// maxOpportunisticBufferSize bounds the non-framed accumulation path. Framed
// protocols are already bounded by maxFrameSize; non-framed binary protocols
// still need a ceiling so a client cannot drip unmatched bytes indefinitely.
const maxOpportunisticBufferSize = 4 << 20 // 4 MiB

var errOpportunisticBufferExceeded = errors.New("tcp opportunistic read buffer exceeded")
var errInvalidFrame = errors.New("invalid TCP frame length")

// connReader reads one logical protocol message at a time from a TCP connection.
// It keeps a buffer of bytes read past the current message (pipelined data) so
// the next call returns them instead of dropping them.
type connReader struct {
	conn         net.Conn
	framing      *parser.Framing
	wireEncoding string
	// cutoff is the absolute connection deadline. The opportunistic accumulation
	// path temporarily lowers the read deadline to a short grace window, then
	// restores it to cutoff — it never extends past cutoff, so the grace window
	// cannot be abused to keep a connection alive indefinitely. The zero value
	// means "no deadline".
	cutoff time.Time
	buf    []byte
}

// nextMessage returns the next logical message. With a Framing spec it reads
// exactly one configured frame (handling split reads and pipelining);
// otherwise it accumulates opportunistically: it returns as soon as any handler
// matches and keeps waiting in short intervals when nothing matches yet, until
// the absolute connection deadline or EOF. Because it stops the instant a
// handler matches, a config with a catch-all handler never incurs extra latency.
func (r *connReader) nextMessage(commands []parser.Command) ([]byte, error) {
	if r.framing != nil {
		return r.nextFramed()
	}
	return r.nextOpportunistic(commands)
}

func (r *connReader) fill() error {
	tmp := make([]byte, 4096)
	n, err := r.conn.Read(tmp)
	if n > 0 {
		r.buf = append(r.buf, tmp[:n]...)
	}
	return err
}

func (r *connReader) takeBuffer() []byte {
	msg := r.buf
	r.buf = nil
	return msg
}

// fillUntil reads until the buffer holds at least n bytes. It returns an error
// only if the buffer is still short after a read failed, so bytes delivered in
// the same Read as io.EOF (a client that sends a full message then closes) are
// used rather than discarded.
func (r *connReader) fillUntil(n int) error {
	for len(r.buf) < n {
		err := r.fill()
		if len(r.buf) >= n {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// validFraming reports whether the framing spec is usable; a malformed spec
// (e.g. lengthSize 0 or >8) would otherwise mis-slice the stream.
func validFraming(f *parser.Framing) bool {
	if f == nil {
		return false
	}
	switch f.Mode {
	case "ber":
		return true
	case "fixed":
		return f.FixedSize > 0 && f.FixedSize <= maxFrameSize
	case "varint-length-prefix":
		return f.LengthOffset >= 0 && f.LengthOffset <= maxFrameSize &&
			f.MaxLengthBytes >= 1 && f.MaxLengthBytes <= 8 &&
			f.LengthOffset <= maxFrameSize-f.MaxLengthBytes
	case "", "length-prefix":
		return f.LengthOffset >= 0 && f.LengthOffset <= maxFrameSize &&
			f.LengthSize >= 1 && f.LengthSize <= 8 &&
			f.HeaderSize >= 0 && f.HeaderSize <= maxFrameSize &&
			f.LengthOffset <= maxFrameSize-f.LengthSize
	default:
		return false
	}
}

func (r *connReader) nextFramed() ([]byte, error) {
	f := r.framing
	if !validFraming(f) {
		return nil, errInvalidFrame
	}
	switch f.Mode {
	case "ber":
		return r.nextBERFrame()
	case "fixed":
		return r.nextFixedFrame()
	case "varint-length-prefix":
		return r.nextVarintLengthFrame()
	}

	if f.LengthOffset > maxFrameSize-f.LengthSize {
		return nil, errInvalidFrame
	}
	headerEnd := f.LengthOffset + f.LengthSize
	if err := r.fillUntil(headerEnd); err != nil {
		return r.takeBuffer(), err
	}
	length := 0
	for i := 0; i < f.LengthSize; i++ {
		idx := f.LengthOffset + i
		if !f.BigEndian {
			idx = f.LengthOffset + (f.LengthSize - 1 - i)
		}
		if length > maxFrameSize>>8 {
			return r.takeBuffer(), errInvalidFrame
		}
		length = (length << 8) | int(r.buf[idx])
	}
	total := length
	if !f.LengthIncludesHeader {
		if length > maxFrameSize-f.HeaderSize {
			return r.takeBuffer(), errInvalidFrame
		}
		total = f.HeaderSize + length
	}
	// total must be at least the header we already consumed; a smaller value is
	// bogus and would slice inside the header, desyncing the stream.
	if total < headerEnd || total > maxFrameSize {
		// Return the bytes already received for telemetry together with an error.
		return r.takeBuffer(), errInvalidFrame
	}
	if err := r.fillUntil(total); err != nil {
		return r.takeBuffer(), err
	}
	msg := r.buf[:total]
	// Copy the leftover so the returned msg doesn't alias the retained buffer's
	// backing array (and so the array isn't kept alive by a small leftover).
	r.buf = append([]byte(nil), r.buf[total:]...)
	return msg, nil
}

func (r *connReader) nextFixedFrame() ([]byte, error) {
	total := r.framing.FixedSize
	if err := r.fillUntil(total); err != nil {
		return r.takeBuffer(), err
	}
	msg := append([]byte(nil), r.buf[:total]...)
	r.buf = append([]byte(nil), r.buf[total:]...)
	return msg, nil
}

// nextVarintLengthFrame reads an unsigned base-128 little-endian length field.
// The bytes before LengthOffset are part of the frame, the encoded varint ends
// the header, and the decoded value is the number of payload bytes that follow.
func (r *connReader) nextVarintLengthFrame() ([]byte, error) {
	f := r.framing
	value := 0
	multiplier := 1
	for i := 0; i < f.MaxLengthBytes; i++ {
		lengthByteIndex := f.LengthOffset + i
		if err := r.fillUntil(lengthByteIndex + 1); err != nil {
			return r.takeBuffer(), err
		}
		encoded := r.buf[lengthByteIndex]
		part := int(encoded & 0x7f)
		if part > (maxFrameSize-value)/multiplier {
			return r.takeBuffer(), errInvalidFrame
		}
		value += part * multiplier
		if encoded&0x80 == 0 {
			headerEnd := lengthByteIndex + 1
			if value > maxFrameSize-headerEnd {
				return r.takeBuffer(), errInvalidFrame
			}
			total := headerEnd + value
			if err := r.fillUntil(total); err != nil {
				return r.takeBuffer(), err
			}
			msg := append([]byte(nil), r.buf[:total]...)
			r.buf = append([]byte(nil), r.buf[total:]...)
			return msg, nil
		}
		if i == f.MaxLengthBytes-1 || multiplier > maxFrameSize/128 {
			return r.takeBuffer(), errInvalidFrame
		}
		multiplier *= 128
	}
	return r.takeBuffer(), errInvalidFrame
}

func (r *connReader) nextBERFrame() ([]byte, error) {
	if err := r.fillUntil(2); err != nil {
		return r.takeBuffer(), err
	}
	lengthByte := r.buf[1]
	lengthBytes := 1
	contentLength := 0
	if lengthByte&0x80 == 0 {
		contentLength = int(lengthByte)
	} else {
		count := int(lengthByte & 0x7f)
		if count == 0 || count > 8 {
			return r.takeBuffer(), errInvalidFrame
		}
		if err := r.fillUntil(2 + count); err != nil {
			return r.takeBuffer(), err
		}
		lengthBytes += count
		for i := 0; i < count; i++ {
			if contentLength > maxFrameSize>>8 {
				return r.takeBuffer(), errInvalidFrame
			}
			contentLength = (contentLength << 8) | int(r.buf[2+i])
		}
	}
	total := 1 + lengthBytes + contentLength
	if total < 2 || total > maxFrameSize {
		return r.takeBuffer(), errInvalidFrame
	}
	if err := r.fillUntil(total); err != nil {
		return r.takeBuffer(), err
	}
	msg := append([]byte(nil), r.buf[:total]...)
	r.buf = append([]byte(nil), r.buf[total:]...)
	return msg, nil
}

func (r *connReader) nextOpportunistic(commands []parser.Command) ([]byte, error) {
	// The loop has three phases: perform the initial blocking read, use one short
	// grace window to reassemble a split message, then restore the absolute
	// connection deadline for any remaining wait. Every terminal path that has
	// buffered bytes returns them through takeBuffer(), including EOF-with-data.
	if len(r.buf) == 0 {
		// Only surface the error if nothing was read; bytes delivered alongside
		// io.EOF must still be processed.
		if err := r.fill(); err != nil && len(r.buf) == 0 {
			return nil, err
		}
		if len(r.buf) > maxOpportunisticBufferSize {
			return nil, errOpportunisticBufferExceeded
		}
	}
	for !matchesAny(r.buf, commands, r.wireEncoding) {
		if len(r.buf) > maxOpportunisticBufferSize {
			return nil, errOpportunisticBufferExceeded
		}
		// Nothing matches yet: the message may be split across TCP segments, so
		// briefly wait for more bytes before returning what we have (which then
		// falls through to the not_found path). A config with a catch-all never
		// reaches here, so it incurs no extra latency. The grace deadline never
		// extends past the absolute connection cutoff.
		grace := time.Now().Add(accumulationGrace)
		if !r.cutoff.IsZero() && r.cutoff.Before(grace) {
			grace = r.cutoff
		}
		if err := r.conn.SetReadDeadline(grace); err != nil {
			return r.takeBuffer(), err
		}
		before := len(r.buf)
		err := r.fill()
		if deadlineErr := r.conn.SetReadDeadline(r.cutoff); deadlineErr != nil && err == nil {
			return r.takeBuffer(), deadlineErr
		}
		if len(r.buf) > maxOpportunisticBufferSize {
			return nil, errOpportunisticBufferExceeded
		}
		if len(r.buf) == before {
			if timeoutErr, ok := err.(net.Error); ok && timeoutErr.Timeout() {
				// A TCP segment can be delayed longer than accumulationGrace. After
				// the first grace timeout, block on the restored absolute deadline
				// (or with no deadline) instead of waking on a timer every grace
				// interval for the lifetime of an idle connection.
				err = r.fill()
				if len(r.buf) > maxOpportunisticBufferSize {
					return nil, errOpportunisticBufferExceeded
				}
				if len(r.buf) > before {
					if err != nil {
						break
					}
					continue
				}
				if cutoffErr, ok := err.(net.Error); ok && cutoffErr.Timeout() {
					return r.takeBuffer(), err
				}
			}
			// EOF or another terminal condition: deliver the buffered bytes.
			break
		}
		if err != nil {
			// The peer may have closed after sending final unmatched bytes.
			// Keep those bytes and let the caller decide how to handle them.
			break
		}
	}
	return r.takeBuffer(), nil
}

// matchesAny reports whether any configured handler matches the bytes so far.
func matchesAny(raw []byte, commands []parser.Command, wireEncoding string) bool {
	input := commandMatchInput(raw, wireEncoding)
	for i := range commands {
		if commands[i].Regex != nil && commands[i].Regex.MatchString(input) {
			return true
		}
	}
	return false
}
