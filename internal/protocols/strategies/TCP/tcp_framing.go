package TCP

import (
	"net"
	"strings"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
)

// accumulationGrace is how long the opportunistic reader waits for more bytes
// when only a catch-all handler would match the data so far. Short enough not
// to add noticeable latency, long enough to reassemble a split binary message.
const accumulationGrace = 150 * time.Millisecond

// maxFrameSize bounds a single length-prefixed frame to avoid huge allocations
// driven by a malicious/garbage length field.
const maxFrameSize = 1 << 20 // 1 MiB

// connReader reads one logical protocol message at a time from a TCP connection.
// It keeps a buffer of bytes read past the current message (pipelined data) so
// the next call returns them instead of dropping them.
type connReader struct {
	conn    net.Conn
	framing *parser.Framing
	// cutoff is the absolute connection deadline. The opportunistic accumulation
	// path temporarily lowers the read deadline to a short grace window, then
	// restores it to cutoff — it never extends past cutoff, so the grace window
	// cannot be abused to keep a connection alive indefinitely. The zero value
	// means "no deadline".
	cutoff time.Time
	buf    []byte
}

// nextMessage returns the next logical message. With a Framing spec it reads
// exactly one length-prefixed frame (handling split reads and pipelining);
// otherwise it accumulates opportunistically: it returns as soon as any handler
// matches, and only waits (up to accumulationGrace) when nothing matches yet, in
// case the message is split across TCP segments. Because it stops the instant a
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
	return f.LengthOffset >= 0 && f.LengthSize >= 1 && f.LengthSize <= 8 && f.HeaderSize >= 0
}

func (r *connReader) nextFramed() ([]byte, error) {
	f := r.framing
	if !validFraming(f) {
		// Misconfigured framing: degrade to a single read so the handler/catch_all
		// can still respond rather than mis-framing every message.
		if err := r.fill(); err != nil && len(r.buf) == 0 {
			return nil, err
		}
		msg := r.buf
		r.buf = nil
		return msg, nil
	}
	headerEnd := f.LengthOffset + f.LengthSize
	if err := r.fillUntil(headerEnd); err != nil {
		return nil, err
	}
	length := 0
	for i := 0; i < f.LengthSize; i++ {
		idx := f.LengthOffset + i
		if !f.BigEndian {
			idx = f.LengthOffset + (f.LengthSize - 1 - i)
		}
		length = (length << 8) | int(r.buf[idx])
	}
	total := length
	if !f.LengthIncludesHeader {
		total = f.HeaderSize + length
	}
	// total must be at least the header we already consumed; a smaller value is
	// bogus and would slice inside the header, desyncing the stream.
	if total < headerEnd || total > maxFrameSize {
		// Bogus length: fall back to returning what we have so the handler/
		// catch_all can still respond, rather than looping forever.
		msg := r.buf
		r.buf = nil
		return msg, nil
	}
	if err := r.fillUntil(total); err != nil {
		return nil, err
	}
	msg := r.buf[:total]
	// Copy the leftover so the returned msg doesn't alias the retained buffer's
	// backing array (and so the array isn't kept alive by a small leftover).
	r.buf = append([]byte(nil), r.buf[total:]...)
	return msg, nil
}

func (r *connReader) nextOpportunistic(commands []parser.Command) ([]byte, error) {
	if len(r.buf) == 0 {
		// Only surface the error if nothing was read; bytes delivered alongside
		// io.EOF must still be processed.
		if err := r.fill(); err != nil && len(r.buf) == 0 {
			return nil, err
		}
	}
	for !matchesAny(r.buf, commands) {
		// Nothing matches yet: the message may be split across TCP segments, so
		// briefly wait for more bytes before returning what we have (which then
		// falls through to the not_found path). A config with a catch-all never
		// reaches here, so it incurs no extra latency. The grace deadline never
		// extends past the absolute connection cutoff.
		grace := time.Now().Add(accumulationGrace)
		if !r.cutoff.IsZero() && r.cutoff.Before(grace) {
			grace = r.cutoff
		}
		r.conn.SetReadDeadline(grace)
		before := len(r.buf)
		err := r.fill()
		r.conn.SetReadDeadline(r.cutoff) // restore absolute cutoff (zero = none)
		if len(r.buf) == before {
			// No progress within the grace window; deliver the bytes already
			// buffered to the caller's not_found/catch-all path.
			break
		}
		if err != nil {
			// The peer may have closed after sending final unmatched bytes.
			// Keep those bytes and let the caller decide how to handle them.
			break
		}
	}
	msg := r.buf
	r.buf = nil
	return msg, nil
}

// matchesAny reports whether any configured handler matches the bytes so far.
func matchesAny(raw []byte, commands []parser.Command) bool {
	input := strings.TrimRight(rawBytesToLatin1(raw), "\r\n")
	for i := range commands {
		if commands[i].Regex != nil && commands[i].Regex.MatchString(input) {
			return true
		}
	}
	return false
}
