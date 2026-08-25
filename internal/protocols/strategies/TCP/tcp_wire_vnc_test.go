package TCP

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"gopkg.in/yaml.v3"
)

// vncTracer is a concurrency-safe tracer that records emitted events.
type vncTracer struct {
	mu     sync.Mutex
	events []tracer.Event
}

func (v *vncTracer) TraceEvent(e tracer.Event) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.events = append(v.events, e)
}

func (v *vncTracer) snapshot() []tracer.Event {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]tracer.Event, len(v.events))
	copy(out, v.events)
	return out
}

// vncStartService loads the given service YAML, binds it to a free local port,
// and starts the TCP strategy. Self-contained so this test compiles without the
// private compatibility test helpers.
func vncStartService(t *testing.T, yamlPath string) (string, *vncTracer) {
	t.Helper()
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read config %s: %v", yamlPath, err)
	}
	var cfg parser.BeelzebubServiceConfiguration
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal %s: %v", yamlPath, err)
	}
	if err := cfg.CompileCommandRegex(); err != nil {
		t.Fatalf("compile regexes: %v", err)
	}
	cfg.DeadlineTimeoutSeconds = 5

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	cfg.Address = ln.Addr().String()
	ln.Close()

	tr := &vncTracer{}
	s := &TCPStrategy{}
	if err := s.Init(cfg, tr); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	})
	time.Sleep(30 * time.Millisecond)
	return cfg.Address, tr
}

// TestVNC_ConnIDIsolation verifies two concurrent connections sharing a source
// IP (same SessionKey) keep independent challenge state, keyed by ConnID. Before
// the ConnID fix, the second connection's challenge clobbered the first's.
func TestVNC_ConnIDIsolation(t *testing.T) {
	p := &vncWirePlugin{}
	challengeHandler := plugin.WireCommand{Name: vncChallengeHandler}
	responseHandler := plugin.WireCommand{Name: vncResponseHandler}

	chA := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	chB := []byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	respA := make([]byte, 16)
	respB := make([]byte, 16)
	for i := range respA {
		respA[i] = byte(0xA0 + i)
		respB[i] = byte(0xB0 + i)
	}

	store := func(connID string, ch []byte) {
		out := append([]byte(nil), ch...)
		if err := p.OnExchange(context.Background(), &plugin.WireContext{ConnID: connID, SessionKey: "TCP9.9.9.9", Command: challengeHandler, Response: out}); err != nil {
			t.Fatalf("store challenge: %v", err)
		}
	}
	capture := func(connID string, resp []byte) map[string]string {
		exchange := &plugin.WireContext{ConnID: connID, SessionKey: "TCP9.9.9.9", Command: responseHandler, Request: resp}
		if err := p.OnExchange(context.Background(), exchange); err != nil {
			t.Fatalf("capture response: %v", err)
		}
		return exchange.Metadata
	}

	// Interleave two connections from the same source IP.
	store("conn-A", chA)
	store("conn-B", chB)
	evB := capture("conn-B", respB)
	evA := capture("conn-A", respA)

	if evA["vnc_challenge"] != hex.EncodeToString(chA) {
		t.Errorf("conn-A challenge = %s, want %s (cross-connection clobber)", evA["vnc_challenge"], hex.EncodeToString(chA))
	}
	if evB["vnc_challenge"] != hex.EncodeToString(chB) {
		t.Errorf("conn-B challenge = %s, want %s (cross-connection clobber)", evB["vnc_challenge"], hex.EncodeToString(chB))
	}
}

// TestVNCCompat_AuthCredentialCapture drives the full RFB 3.8 VNC Authentication
// handshake against the shipped tcp-5900-vnc.yaml and asserts the wire-plugin
// captured the challenge/response pair into Event.Metadata.
func TestVNCCompat_AuthCredentialCapture(t *testing.T) {
	addr, tr := vncStartService(t, "../../../../configurations/services/tcp-5900-vnc.yaml")

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(4 * time.Second))

	// 1. Server sends the 12-byte ProtocolVersion banner.
	banner := make([]byte, 12)
	if _, err := io.ReadFull(conn, banner); err != nil {
		t.Fatalf("read banner: %v", err)
	}
	if string(banner) != "RFB 003.008\n" {
		t.Fatalf("banner = %q, want \"RFB 003.008\\n\"", banner)
	}

	// 2. Client echoes the ProtocolVersion → server replies security types.
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatalf("write version: %v", err)
	}
	secTypes := make([]byte, 2)
	if _, err := io.ReadFull(conn, secTypes); err != nil {
		t.Fatalf("read security types: %v", err)
	}
	// [count=1][type=2 (VNC Authentication)]
	if secTypes[0] != 0x01 || secTypes[1] != 0x02 {
		t.Fatalf("security types = % x, want 01 02", secTypes)
	}

	// 3. Client selects VNC Authentication → server sends 16-byte challenge.
	if _, err := conn.Write([]byte{0x02}); err != nil {
		t.Fatalf("write sec-type selection: %v", err)
	}
	challenge := make([]byte, 16)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		t.Fatalf("read challenge: %v", err)
	}

	// 4. Client sends the 16-byte DES response → server replies SecurityResult.
	response := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0x11, 0x22, 0x33, 0x44,
		0x55, 0x66, 0x77, 0x88, 0x99, 0x00, 0xFE, 0xED}
	if _, err := conn.Write(response); err != nil {
		t.Fatalf("write response: %v", err)
	}
	secResult := make([]byte, 4)
	if _, err := io.ReadFull(conn, secResult); err != nil {
		t.Fatalf("read SecurityResult: %v", err)
	}
	if secResult[3] != 0x01 {
		t.Fatalf("SecurityResult = % x, want 00 00 00 01 (failed)", secResult)
	}
	// RFB 3.8: a failed SecurityResult is followed by a 4-byte reason length and
	// the reason text. A conformant client requires this.
	reasonLen := make([]byte, 4)
	if _, err := io.ReadFull(conn, reasonLen); err != nil {
		t.Fatalf("read reason length: %v", err)
	}
	n := int(reasonLen[0])<<24 | int(reasonLen[1])<<16 | int(reasonLen[2])<<8 | int(reasonLen[3])
	if n <= 0 || n > 256 {
		t.Fatalf("implausible reason length %d", n)
	}
	reason := make([]byte, n)
	if _, err := io.ReadFull(conn, reason); err != nil {
		t.Fatalf("read reason text: %v", err)
	}
	if string(reason) != "Authentication failed" {
		t.Errorf("reason = %q, want \"Authentication failed\"", reason)
	}

	// Allow the trace event to be recorded.
	time.Sleep(50 * time.Millisecond)

	// Verify the wire-plugin captured the credential material.
	var got *tracer.Event
	for _, e := range tr.snapshot() {
		if e.Metadata["vnc_response"] != "" {
			ev := e
			got = &ev
			break
		}
	}
	if got == nil {
		t.Fatal("no event with Metadata[vnc_response] — wire-plugin did not capture")
	}
	if got.Metadata["vnc_challenge"] != hex.EncodeToString(challenge) {
		t.Errorf("captured challenge %s != sent challenge %s",
			got.Metadata["vnc_challenge"], hex.EncodeToString(challenge))
	}
	if got.Metadata["vnc_response"] != hex.EncodeToString(response) {
		t.Errorf("captured response %s != sent response %s",
			got.Metadata["vnc_response"], hex.EncodeToString(response))
	}
	wantJohn := "$vnc$*" + hex.EncodeToString(challenge) + "*" + hex.EncodeToString(response)
	if got.Metadata["vnc_john"] != wantJohn {
		t.Errorf("vnc_john = %s, want %s", got.Metadata["vnc_john"], wantJohn)
	}
	t.Logf("captured John hash: %s", got.Metadata["vnc_john"])
}
