package TCP

import (
	"io"
	"net"
	"regexp"
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

// TestFraming_BogusLength falls back to returning buffered bytes rather than
// blocking forever on an absurd length.
func TestFraming_BogusLength(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 0, LengthSize: 4, BigEndian: true, HeaderSize: 4}
	r := newReader(server, f)

	// Length 0xFFFFFFFF > maxFrameSize → fall back.
	writeThenClose(t, client, 0, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x01})
	msg, err := r.nextMessage(nil)
	if err != nil {
		t.Fatalf("nextMessage err: %v", err)
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
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Fallback returns the whole buffer rather than a 2-byte mis-slice.
	if len(msg) < 4 {
		t.Fatalf("mis-sliced inside header: got %d bytes, want full-buffer fallback", len(msg))
	}
}

// TestFraming_InvalidSpecDegrades verifies a malformed framing spec (lengthSize 0)
// degrades to a single read instead of mis-framing.
func TestFraming_InvalidSpecDegrades(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	f := &parser.Framing{LengthOffset: 0, LengthSize: 0} // invalid
	r := newReader(server, f)
	writeThenClose(t, client, 0, []byte("hello"))
	msg, err := r.nextMessage(nil)
	if err != nil || string(msg) != "hello" {
		t.Fatalf("msg = %q err = %v, want \"hello\"", msg, err)
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

	writeThenClose(t, client, 30*time.Millisecond, []byte("ABCD"), []byte("EFGH"))
	msg, err := r.nextMessage(cmds)
	if err != nil {
		t.Fatalf("nextMessage err: %v", err)
	}
	if string(msg) != "ABCDEFGH" {
		t.Fatalf("accumulated msg = %q, want ABCDEFGH", msg)
	}
}
