package TCP

import (
	"bytes"
	"errors"
	"io"
	"net"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
)

// writeThenClose writes the given chunks to one end of a pipe, sleeping between
// them to simulate distinct TCP segments, then closes.
func writeThenClose(t *testing.T, c net.Conn, gap time.Duration, chunks ...[]byte) {
	t.Helper()
	go func() {
		for _, ch := range chunks {
			c.Write(ch)
			if gap > 0 {
				time.Sleep(gap)
			}
		}
		c.Close()
	}()
}

func newReader(server net.Conn, framing *parser.Framing) *connReader {
	cutoff := time.Now().Add(5 * time.Second)
	server.SetDeadline(cutoff)
	return &connReader{conn: server, framing: framing, cutoff: cutoff}
}

func framingTestBERTLV(tag byte, value []byte) []byte {
	length := len(value)
	if length < 128 {
		return append([]byte{tag, byte(length)}, value...)
	}
	var encoded [8]byte
	i := len(encoded)
	for n := length; n > 0; n >>= 8 {
		i--
		encoded[i] = byte(n)
	}
	header := append([]byte{tag, 0x80 | byte(len(encoded)-i)}, encoded[i:]...)
	return append(header, value...)
}

// TestFraming_BigEndian_SingleFrame reads a TPKT-style frame: 16-bit big-endian
// length at offset 2 that counts the whole PDU (lengthIncludesHeader).
func TestFraming_BigEndian_SingleFrame(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 2, LengthSize: 2, BigEndian: true, LengthIncludesHeader: true}
	r := newReader(server, f)

	// TPKT: 03 00 <len:2> ...payload. Total length 8.
	frame := []byte{0x03, 0x00, 0x00, 0x08, 0xAA, 0xBB, 0xCC, 0xDD}
	writeThenClose(t, client, 0, frame)

	msg, err := r.nextMessage(nil)
	if err != nil {
		t.Fatalf("nextMessage err: %v", err)
	}
	if len(msg) != 8 {
		t.Fatalf("frame len = %d, want 8 (%v)", len(msg), msg)
	}
}

// TestFraming_SplitRead reassembles a frame delivered in two segments.
func TestFraming_SplitRead(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 2, LengthSize: 2, BigEndian: true, LengthIncludesHeader: true}
	r := newReader(server, f)

	writeThenClose(t, client, 20*time.Millisecond,
		[]byte{0x03, 0x00, 0x00}, // length field split across segments
		[]byte{0x08, 0xAA, 0xBB}, // rest of header + partial payload
		[]byte{0xCC, 0xDD})       // remaining payload
	msg, err := r.nextMessage(nil)
	if err != nil {
		t.Fatalf("nextMessage err: %v", err)
	}
	if len(msg) != 8 {
		t.Fatalf("reassembled frame len = %d, want 8", len(msg))
	}
}

// TestFraming_Pipelined returns one frame per call when two are sent back-to-back.
func TestFraming_Pipelined(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 0, LengthSize: 4, BigEndian: false} // 32-bit LE length prefix, payload follows header
	f.HeaderSize = 4
	r := newReader(server, f)

	// Two frames: each = 4-byte LE length (of payload) + payload.
	frame1 := []byte{0x02, 0x00, 0x00, 0x00, 0x11, 0x22}
	frame2 := []byte{0x03, 0x00, 0x00, 0x00, 0x33, 0x44, 0x55}
	writeThenClose(t, client, 0, append(append([]byte{}, frame1...), frame2...))

	m1, err := r.nextMessage(nil)
	if err != nil || len(m1) != 6 {
		t.Fatalf("frame1 len = %d err=%v, want 6", len(m1), err)
	}
	m2, err := r.nextMessage(nil)
	if err != nil || len(m2) != 7 {
		t.Fatalf("frame2 len = %d err=%v, want 7", len(m2), err)
	}
}

// TestFraming_BogusLength returns the buffered prefix with an explicit error
// rather than silently accepting an absurd length.
func TestFraming_BogusLength(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 0, LengthSize: 4, BigEndian: true, HeaderSize: 4}
	r := newReader(server, f)

	// Length 0xFFFFFFFF > maxFrameSize → fall back.
	writeThenClose(t, client, 0, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x01})
	msg, err := r.nextMessage(nil)
	if !errors.Is(err, errInvalidFrame) {
		t.Fatalf("nextMessage err = %v, want %v", err, errInvalidFrame)
	}
	if len(msg) == 0 {
		t.Fatal("expected fallback to return buffered bytes")
	}
}

// eofWithDataConn is a net.Conn whose first Read returns all the data together
// with io.EOF (a real kernel behavior when a peer sends then immediately closes).
type eofWithDataConn struct {
	net.Conn
	data []byte
	done bool
}

func (c *eofWithDataConn) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	c.done = true
	n := copy(p, c.data)
	return n, io.EOF // data AND EOF in the same Read
}
func (c *eofWithDataConn) SetReadDeadline(time.Time) error { return nil }

// TestFraming_DataDeliveredWithEOF verifies a complete frame arriving in the same
// Read as io.EOF is returned, not discarded (regression: fill() returned the err
// before the buffered bytes were used).
func TestFraming_DataDeliveredWithEOF(t *testing.T) {
	f := &parser.Framing{LengthOffset: 2, LengthSize: 2, BigEndian: true, LengthIncludesHeader: true}
	conn := &eofWithDataConn{data: []byte{0x03, 0x00, 0x00, 0x06, 0xAA, 0xBB}}
	r := &connReader{conn: conn, framing: f}
	msg, err := r.nextMessage(nil)
	if err != nil {
		t.Fatalf("err = %v, want nil (frame must survive EOF)", err)
	}
	if len(msg) != 6 {
		t.Fatalf("frame len = %d, want 6", len(msg))
	}
}

func TestFraming_PartialHeaderDeliveredWithEOF(t *testing.T) {
	f := &parser.Framing{LengthOffset: 2, LengthSize: 2, BigEndian: true, LengthIncludesHeader: true}
	conn := &eofWithDataConn{data: []byte{0x03, 0x00, 0x00}}
	r := &connReader{conn: conn, framing: f}

	msg, err := r.nextMessage(nil)

	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if !bytes.Equal(msg, conn.data) {
		t.Fatalf("partial header = %x, want %x", msg, conn.data)
	}
}

// TestFraming_TotalSmallerThanHeader verifies a length that decodes below the
// header size hits the bogus-length fallback instead of slicing inside the header.
func TestFraming_TotalSmallerThanHeader(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	// lengthIncludesHeader=true, headerEnd=4, but length field = 2 (< 4).
	f := &parser.Framing{LengthOffset: 2, LengthSize: 2, BigEndian: true, LengthIncludesHeader: true}
	r := newReader(server, f)
	writeThenClose(t, client, 0, []byte{0x03, 0x00, 0x00, 0x02, 0xAA, 0xBB})
	msg, err := r.nextMessage(nil)
	if !errors.Is(err, errInvalidFrame) {
		t.Fatalf("err = %v, want %v", err, errInvalidFrame)
	}
	// Fallback returns the whole buffer rather than a 2-byte mis-slice.
	if len(msg) < 4 {
		t.Fatalf("mis-sliced inside header: got %d bytes, want full-buffer fallback", len(msg))
	}
}

func TestFraming_InvalidSpecIsRejected(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 0, LengthSize: 0} // invalid
	r := newReader(server, f)
	msg, err := r.nextMessage(nil)
	if !errors.Is(err, errInvalidFrame) || msg != nil {
		t.Fatalf("msg = %q err = %v, want invalid-frame error", msg, err)
	}
}

func TestFraming_OverflowingOffsetIsRejected(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	r := &connReader{framing: &parser.Framing{LengthOffset: maxInt, LengthSize: 8}}
	msg, err := r.nextMessage(nil)
	if !errors.Is(err, errInvalidFrame) || msg != nil {
		t.Fatalf("msg = %q err = %v, want invalid-frame error", msg, err)
	}
}

func TestFraming_BERSplitAndPipelined(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	r := newReader(server, &parser.Framing{Mode: "ber"})
	first := framingTestBERTLV(0x30, bytes.Repeat([]byte{0x41}, 130))
	second := framingTestBERTLV(0x30, []byte("second"))
	combined := append(append([]byte(nil), first...), second...)
	writeThenClose(t, client, 20*time.Millisecond, combined[:2], combined[2:50], combined[50:])

	gotFirst, err := r.nextMessage(nil)
	if err != nil || !bytes.Equal(gotFirst, first) {
		t.Fatalf("first BER frame len=%d err=%v", len(gotFirst), err)
	}
	gotSecond, err := r.nextMessage(nil)
	if err != nil || !bytes.Equal(gotSecond, second) {
		t.Fatalf("second BER frame len=%d err=%v", len(gotSecond), err)
	}
}

func TestFraming_BERRejectsIndefiniteLength(t *testing.T) {
	r := &connReader{framing: &parser.Framing{Mode: "ber"}, buf: []byte{0x30, 0x80}}
	msg, err := r.nextMessage(nil)
	if !errors.Is(err, errInvalidFrame) || len(msg) != 2 {
		t.Fatalf("msg=%x err=%v", msg, err)
	}
}

// TestOpportunistic_CatchAllReturnsImmediately ensures a catch-all handler makes
// the reader return on the first read with no accumulation delay.
func TestOpportunistic_CatchAllReturnsImmediately(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	r := newReader(server, nil)
	cmds := []parser.Command{{Regex: regexp.MustCompile(`(?s).*`)}}

	writeThenClose(t, client, 0, []byte("hello"))
	start := time.Now()
	msg, err := r.nextMessage(cmds)
	elapsed := time.Since(start)
	if err != nil || string(msg) != "hello" {
		t.Fatalf("msg = %q err=%v, want \"hello\"", msg, err)
	}
	if elapsed >= accumulationGrace {
		t.Errorf("catch-all incurred accumulation delay: %v", elapsed)
	}
}

// TestOpportunistic_AccumulatesUntilMatch reassembles a message split across
// segments when only a specific (anchored) handler — no catch-all — matches.
func TestOpportunistic_AccumulatesUntilMatch(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	r := newReader(server, nil)
	// Matches only the complete 8-byte message.
	cmds := []parser.Command{{Regex: regexp.MustCompile(`(?s)^.{8}$`)}}

	writeThenClose(t, client, accumulationGrace+50*time.Millisecond, []byte("ABCD"), []byte("EFGH"))
	msg, err := r.nextMessage(cmds)
	if err != nil {
		t.Fatalf("nextMessage err: %v", err)
	}
	if string(msg) != "ABCDEFGH" {
		t.Fatalf("accumulated msg = %q, want ABCDEFGH", msg)
	}
}

type deadlineCountingConn struct {
	net.Conn
	mu               sync.Mutex
	nonZeroDeadlines int
}

func (c *deadlineCountingConn) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		c.mu.Lock()
		c.nonZeroDeadlines++
		c.mu.Unlock()
	}
	return c.Conn.SetReadDeadline(deadline)
}

func (c *deadlineCountingConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nonZeroDeadlines
}

func TestOpportunistic_IdlePartialMessageDoesNotPollGraceDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	counted := &deadlineCountingConn{Conn: server}
	r := &connReader{conn: counted}
	cmds := []parser.Command{{Regex: regexp.MustCompile(`^ABCDEFGH$`)}}

	writeThenClose(t, client, 2*accumulationGrace+50*time.Millisecond, []byte("ABCD"), []byte("EFGH"))
	msg, err := r.nextMessage(cmds)
	if err != nil || string(msg) != "ABCDEFGH" {
		t.Fatalf("msg = %q err=%v, want ABCDEFGH", msg, err)
	}
	if got := counted.count(); got != 1 {
		t.Fatalf("non-zero read deadlines = %d, want 1; idle partial reads must block after the grace period", got)
	}
}

func TestOpportunistic_BufferCap(t *testing.T) {
	r := &connReader{buf: bytes.Repeat([]byte("A"), maxOpportunisticBufferSize+1)}
	cmds := []parser.Command{{Regex: regexp.MustCompile(`^never$`)}}

	msg, err := r.nextMessage(cmds)
	if !errors.Is(err, errOpportunisticBufferExceeded) {
		t.Fatalf("err = %v, want %v", err, errOpportunisticBufferExceeded)
	}
	if msg != nil {
		t.Fatalf("msg = %d bytes, want nil on buffer cap", len(msg))
	}
}
