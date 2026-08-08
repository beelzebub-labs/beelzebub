package TCP

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	})
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
	if cert.Leaf == nil || cert.Leaf.IsCA {
		t.Fatal("TLS certificate must be a parsed end-entity certificate")
	}
	if cert.Leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatalf("ECDSA key usage = %v, want digitalSignature only", cert.Leaf.KeyUsage)
	}
	if len(cert.Leaf.ExtKeyUsage) != 1 || cert.Leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("extended key usage = %v, want serverAuth", cert.Leaf.ExtKeyUsage)
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

func TestTLSUpgradeDoesNotExtendAbsoluteDeadline(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	cert, err := generateSelfSignedCert("deadline.test")
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	session := &tcpSession{
		servConf: parser.BeelzebubServiceConfiguration{DeadlineTimeoutSeconds: 1},
		tlsState: &tlsCertificateState{cert: cert, commonName: "deadline.test"},
		cutoff:   time.Now().Add(50 * time.Millisecond),
	}

	upgraded := make(chan bool, 1)
	go func() {
		defer server.Close()
		_, ok := session.upgradeTLS(server)
		upgraded <- ok
	}()

	time.Sleep(100 * time.Millisecond)
	tlsClient := tls.Client(client, &tls.Config{InsecureSkipVerify: true})
	_ = tlsClient.SetDeadline(time.Now().Add(time.Second))
	if err := tlsClient.Handshake(); err == nil {
		t.Fatal("TLS handshake succeeded after the connection's absolute deadline")
	}
	if ok := <-upgraded; ok {
		t.Fatal("server extended the absolute deadline during TLS upgrade")
	}
}

func TestTCPStrategy_UsesDistinctTLSCertificatePerService(t *testing.T) {
	strategy := &TCPStrategy{}
	tr := &vncTracer{}
	for _, name := range []string{"ldap.example.invalid", "rdp.example.invalid"} {
		if err := strategy.Init(parser.BeelzebubServiceConfiguration{
			Address:    "127.0.0.1:0",
			ServerName: name,
			Commands: []parser.Command{{
				Regex:      regexp.MustCompile(`^start$`),
				Handler:    "ready",
				TLSUpgrade: true,
			}},
		}, tr); err != nil {
			t.Fatalf("Init %s: %v", name, err)
		}
	}
	t.Cleanup(func() { _ = strategy.Shutdown() })

	for i, wantName := range []string{"ldap.example.invalid", "rdp.example.invalid"} {
		conn, err := net.DialTimeout("tcp", strategy.listeners[i].Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", wantName, err)
		}
		if _, err := conn.Write([]byte("start")); err != nil {
			t.Fatalf("write %s: %v", wantName, err)
		}
		response := make([]byte, len("ready"))
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatalf("read %s: %v", wantName, err)
		}
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
		if err := tlsConn.Handshake(); err != nil {
			t.Fatalf("handshake %s: %v", wantName, err)
		}
		certificates := tlsConn.ConnectionState().PeerCertificates
		if len(certificates) != 1 {
			t.Fatalf("service %d presented %d certificates, want 1", i, len(certificates))
		}
		if gotName := certificates[0].Subject.CommonName; gotName != wantName {
			t.Fatalf("service %d certificate CN = %q, want %q", i, gotName, wantName)
		}
		tlsConn.Close()
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
	cert, err := generateSelfSignedCert("DC01.company.local")
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	cert.Leaf.NotAfter = time.Now().Add(-time.Hour) // force expired
	state := &tlsCertificateState{cert: cert, commonName: "DC01.company.local"}

	got := state.current()
	if got == nil {
		t.Fatal("nil cert")
	}
	if !got.Leaf.NotAfter.After(time.Now()) {
		t.Error("expected a regenerated, non-expired certificate")
	}
	// a still-valid cert must be returned unchanged (not regenerated)
	if again := state.current(); again != got {
		t.Error("valid cert should not be regenerated")
	}
}

func TestTCPStaticResponsePreservesUTF8ByDefault(t *testing.T) {
	addr, _ := startCfg(t, parser.BeelzebubServiceConfiguration{
		Commands: []parser.Command{{
			Regex:   regexp.MustCompile(`^caffè$`),
			Handler: "già pronto",
		}},
	})
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("caffè\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "già pronto" {
		t.Fatalf("UTF-8 response = %q", got)
	}
}

func TestTCPHistoryKeySeparatesServices(t *testing.T) {
	host := "192.0.2.10"
	ldap := tcpHistoryKey(parser.BeelzebubServiceConfiguration{Address: ":389"}, host)
	smb := tcpHistoryKey(parser.BeelzebubServiceConfiguration{Address: ":445"}, host)
	if ldap == smb {
		t.Fatalf("LDAP and SMB history keys collide: %q", ldap)
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
