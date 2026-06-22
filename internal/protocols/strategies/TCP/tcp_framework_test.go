package TCP

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

// mockCmdPlugin is a CommandPlugin used to exercise the TCP plugin-dispatch path.
type mockCmdPlugin struct{}

func (mockCmdPlugin) Metadata() plugin.Metadata { return plugin.Metadata{Name: "MockTCPPlugin"} }
func (mockCmdPlugin) Execute(_ context.Context, _ plugin.CommandRequest) (string, error) {
	return "plugin-output", nil
}

func init() {
	if _, ok := plugin.Get("MockTCPPlugin"); !ok {
		plugin.Register(mockCmdPlugin{})
	}
}

// startCfg binds the given service configuration to a free local port, starts
// the TCP strategy, and returns the address and a capturing tracer.
func startCfg(t *testing.T, cfg parser.BeelzebubServiceConfiguration) (string, *vncTracer) {
	t.Helper()
	if cfg.DeadlineTimeoutSeconds == 0 {
		cfg.DeadlineTimeoutSeconds = 5
	}
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
	time.Sleep(30 * time.Millisecond)
	return cfg.Address, tr
}

func TestToWindowsFileTime(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := toWindowsFileTime(now)
	if len(b) != 8 {
		t.Fatalf("len = %d, want 8", len(b))
	}
	// Decode and check it round-trips back to the same UTC second.
	const epoch = uint64(116444736000000000)
	ft := binary.LittleEndian.Uint64(b)
	gotUnix := int64((ft - epoch) / 1e7)
	if gotUnix != now.Unix() {
		t.Errorf("decoded unix = %d, want %d", gotUnix, now.Unix())
	}
}

func TestGenerateSelfSignedCert(t *testing.T) {
	cert, err := generateSelfSignedCert("DC01.company.local")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("nil/empty certificate")
	}
	// Empty common name falls back to localhost (must still succeed).
	if _, err := generateSelfSignedCert(""); err != nil {
		t.Fatalf("empty CN: %v", err)
	}
}

func TestApplyPatches_RandomAndFiletime(t *testing.T) {
	buf := make([]byte, 16) // all zero
	patched := applyPatches(buf, []parser.Patch{
		{Type: "random", Offset: 0, Length: 8},
		{Type: "filetime", Offset: 8},
	})
	if len(patched) != 16 {
		t.Fatalf("len = %d, want 16", len(patched))
	}
	// random: at least one of the first 8 bytes should now be non-zero.
	allZero := true
	for _, b := range patched[:8] {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("random patch left all-zero bytes")
	}
	// filetime: the 8 bytes at offset 8 decode to a recent time.
	const epoch = uint64(116444736000000000)
	ft := binary.LittleEndian.Uint64(patched[8:16])
	year := time.Unix(int64((ft-epoch)/1e7), 0).UTC().Year()
	if year < 2020 {
		t.Errorf("filetime year = %d, want >= 2020", year)
	}
	// out-of-range offsets must be ignored, not panic.
	_ = applyPatches(buf, []parser.Patch{{Type: "random", Offset: 100, Length: 8}})
	// no-op when no patches.
	if got := applyPatches(buf, nil); len(got) != 16 {
		t.Error("nil patches should return buffer unchanged")
	}
}

func TestHandleTCPConnection_PatchesInResponse(t *testing.T) {
	// 16 zero bytes; first 8 randomised at write time.
	handler := string(make([]byte, 16))
	addr, _ := startCfg(t, parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{{
			Regex:   regexp.MustCompile(`(?s).*`),
			Handler: handler,
			Patches: []parser.Patch{{Type: "random", Offset: 0, Length: 8}},
		}},
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("trigger"))
	resp := make([]byte, 16)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	allZero := true
	for _, b := range resp[:8] {
		if b != 0 {
			allZero = false
		}
	}
	if allZero {
		t.Error("random patch not applied to written response")
	}
}

func TestHandleTCPConnection_CloseAfter(t *testing.T) {
	addr, _ := startCfg(t, parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{{
			Regex:      regexp.MustCompile(`(?s).*`),
			Handler:    "bye",
			CloseAfter: true,
		}},
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("hi"))
	buf := make([]byte, 8)
	n, _ := conn.Read(buf) // reads "bye"
	if string(buf[:n]) != "bye" {
		t.Fatalf("got %q, want bye", buf[:n])
	}
	// closeAfter: the next read must hit EOF (server closed the connection).
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected connection closed after response (closeAfter)")
	}
}

func TestHandleTCPConnection_TLSUpgrade(t *testing.T) {
	addr, _ := startCfg(t, parser.BeelzebubServiceConfiguration{
		ServerName: "DC01.company.local",
		Commands: []parser.Command{{
			Regex:      regexp.MustCompile(`(?s).*`),
			Handler:    "ready",
			TLSUpgrade: true,
		}},
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("hello"))
	buf := make([]byte, 8)
	n, _ := conn.Read(buf)
	if string(buf[:n]) != "ready" {
		t.Fatalf("pre-TLS got %q, want ready", buf[:n])
	}
	// After the cleartext response the server upgrades to TLS; a TLS handshake
	// over the same connection must now succeed.
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake after upgrade failed: %v", err)
	}
	if cs := tlsConn.ConnectionState(); len(cs.PeerCertificates) == 0 {
		t.Error("no peer certificate presented after TLS upgrade")
	}
}

func TestHandleTCPConnection_PluginDispatch(t *testing.T) {
	addr, tr := startCfg(t, parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{{
			Regex:  regexp.MustCompile(`(?s).*`),
			Plugin: "MockTCPPlugin",
			Name:   "viaplugin",
		}},
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("anything"))
	buf := make([]byte, 32)
	n, _ := conn.Read(buf)
	if string(buf[:n]) != "plugin-output" {
		t.Fatalf("got %q, want plugin-output", buf[:n])
	}
	conn.Close()
	time.Sleep(50 * time.Millisecond)
	// the interaction event records the plugin output
	var found bool
	for _, e := range tr.snapshot() {
		if e.CommandOutput == "plugin-output" {
			found = true
		}
	}
	if !found {
		t.Error("no interaction event with plugin output")
	}
}

func TestCurrentTLSCert_RegeneratesOnExpiry(t *testing.T) {
	s := &TCPStrategy{tlsCN: "DC01.company.local"}
	cert, err := generateSelfSignedCert("DC01.company.local")
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	cert.Leaf.NotAfter = time.Now().Add(-time.Hour) // force expired
	s.tlsCert = cert

	got := s.currentTLSCert()
	if got == nil {
		t.Fatal("nil cert")
	}
	if !got.Leaf.NotAfter.After(time.Now()) {
		t.Error("expected a regenerated, non-expired certificate")
	}
	// a still-valid cert must be returned unchanged (not regenerated)
	if again := s.currentTLSCert(); again != got {
		t.Error("valid cert should not be regenerated")
	}
}

// mockBinaryPlugin returns output with a high byte (rune 0xFE) to verify
// binaryOutput: the byte must survive as 0xFE, not be UTF-8 encoded to 0xC3 0xBE.
type mockBinaryPlugin struct{}

func (mockBinaryPlugin) Metadata() plugin.Metadata { return plugin.Metadata{Name: "MockBinaryPlugin"} }
func (mockBinaryPlugin) Execute(_ context.Context, _ plugin.CommandRequest) (string, error) {
	return string([]rune{0xFE, 'O', 'K'}), nil // Latin-1 bytes FE 4F 4B
}

func init() {
	if _, ok := plugin.Get("MockBinaryPlugin"); !ok {
		plugin.Register(mockBinaryPlugin{})
	}
}

func TestHandleTCPConnection_BinaryPluginOutput(t *testing.T) {
	addr, _ := startCfg(t, parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{{
			Regex:        regexp.MustCompile(`(?s).*`),
			Plugin:       "MockBinaryPlugin",
			BinaryOutput: true,
			Name:         "bin",
		}},
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("x"))
	buf := make([]byte, 8)
	n, _ := conn.Read(buf)
	// binaryOutput → raw Latin-1 bytes FE 4F 4B (3 bytes), not UTF-8 (which
	// would encode 0xFE as C3 BE, giving 4 bytes).
	if n != 3 || buf[0] != 0xFE || buf[1] != 'O' || buf[2] != 'K' {
		t.Errorf("got % x (n=%d), want fe 4f 4b", buf[:n], n)
	}
}
