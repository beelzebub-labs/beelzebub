package TCP

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// rawBytesToLatin1 converts a raw byte slice to a Go string by treating each
// byte as the corresponding Latin-1 (ISO-8859-1) codepoint. This normalises
// binary TCP input so that regexp patterns like \xfe (compiled from YAML
// \xHH escapes, which the YAML library encodes as UTF-8 Unicode codepoints)
// match the incoming byte correctly.
func rawBytesToLatin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, v := range b {
		runes[i] = rune(v)
	}
	return string(runes)
}

// latin1ToRawBytes is the inverse: it converts a string whose rune values
// represent raw byte values (as produced by YAML \xHH escape parsing) back
// to a byte slice suitable for writing to a TCP connection.
func latin1ToRawBytes(s string) []byte {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	return b
}

func wireInputString(b []byte, encoding string) string {
	if encoding == "latin1" {
		return rawBytesToLatin1(b)
	}
	return string(b)
}

func commandMatchInput(b []byte, encoding string) string {
	input := wireInputString(b, encoding)
	if encoding == "latin1" {
		return input
	}
	return strings.TrimRight(input, "\r\n")
}

func staticResponseBytes(s, encoding string) []byte {
	if encoding == "latin1" {
		return latin1ToRawBytes(s)
	}
	return []byte(s)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// hexEscapeNonPrintable renders b as printable ASCII, emitting every
// non-printable byte (and the backslash itself) as \xNN. The result is safe to
// store in trace events, JSON, and TEXT columns while remaining reversible to
// the original bytes. The \xNN form matches the escape syntax already used in
// handler/regex fields of the service YAML, so logs read consistently with
// configuration. A literal backslash is also escaped as \x5c, so decoding the
// emitted \xNN stream recovers the exact original byte sequence.
func hexEscapeNonPrintable(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c >= 32 && c <= 126 && c != '\\' {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "\\x%02x", c)
		}
	}
	return sb.String()
}

// toWindowsFileTime encodes t as a Windows FILETIME: 100-nanosecond intervals
// since 1601-01-01 UTC, little-endian 8 bytes.
func toWindowsFileTime(t time.Time) []byte {
	const epoch = uint64(116444736000000000)
	ft := uint64(t.UnixNano()/100) + epoch
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, ft)
	return b
}

// deadlineCutoff returns the absolute deadline for a connection given the
// configured timeout, or the zero time (no deadline) when the timeout is <= 0.
func deadlineCutoff(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d)
}

// applyPatches applies the generic Patches declared on a Command to buf and
// returns the patched copy. Unknown types are left for wire-plugins.
func applyPatches(buf []byte, patches []parser.Patch) []byte {
	if len(patches) == 0 {
		return buf
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	for _, p := range patches {
		switch p.Type {
		case "random":
			if p.Length <= 0 || p.Offset < 0 || p.Length > len(out) || p.Offset > len(out)-p.Length {
				log.Warnf("skipping random patch outside response: offset=%d length=%d response_size=%d", p.Offset, p.Length, len(out))
				continue
			}
			randomBytes := make([]byte, p.Length)
			if _, err := rand.Read(randomBytes); err != nil {
				log.Errorf("random patch: crypto/rand read failed: %v", err)
			} else {
				copy(out[p.Offset:p.Offset+p.Length], randomBytes)
			}
		case "filetime":
			if p.Offset < 0 || p.Offset > len(out)-8 {
				log.Warnf("skipping filetime patch outside response: offset=%d length=8 response_size=%d", p.Offset, len(out))
				continue
			}
			copy(out[p.Offset:], toWindowsFileTime(time.Now()))
		}
	}
	return out
}

type TCPStrategy struct {
	Sessions *historystore.HistoryStore

	lifecycleMu sync.Mutex
	listeners   []net.Listener
	acceptDone  []chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	cleanerOnce sync.Once

	activeMu    sync.Mutex
	activeConns map[net.Conn]struct{}
	handlers    sync.WaitGroup
}

type tlsCertificateState struct {
	mu         sync.Mutex
	cert       *tls.Certificate
	commonName string
	certPath   string
	keyPath    string
}

func (state *tlsCertificateState) current() *tls.Certificate {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cert == nil {
		return nil
	}
	if leaf := state.cert.Leaf; leaf != nil && time.Now().After(leaf.NotAfter) {
		cert, err := state.load()
		if err != nil {
			log.Errorf("TLS certificate reload failed: %v", err)
			return nil
		}
		state.cert = cert
	}
	return state.cert
}

func (state *tlsCertificateState) load() (*tls.Certificate, error) {
	if state.certPath != "" || state.keyPath != "" {
		return loadTLSKeyPair(state.certPath, state.keyPath)
	}
	return generateSelfSignedCert(state.commonName)
}

func newTLSCertificateState(servConf parser.BeelzebubServiceConfiguration) (*tlsCertificateState, error) {
	if (servConf.TLSCertPath == "") != (servConf.TLSKeyPath == "") {
		return nil, errors.New("both tlsCertPath and tlsKeyPath must be set for TLS, or neither")
	}

	state := &tlsCertificateState{
		commonName: servConf.ServerName,
		certPath:   servConf.TLSCertPath,
		keyPath:    servConf.TLSKeyPath,
	}
	cert, err := state.load()
	if err != nil {
		return nil, err
	}
	state.cert = cert
	return state, nil
}

func loadTLSKeyPair(certPath, keyPath string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("loading TLS certificate and key: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("loading TLS certificate and key: certificate chain is empty")
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parsing TLS leaf certificate: %w", err)
	}
	return &cert, nil
}

// generateSelfSignedCert creates an ephemeral P-256 ECDSA self-signed certificate
// used for TLS upgrade on protocols that require it.
// commonName is taken from the service configuration's serverName field so that
// each honeypot presents a cert matching its declared identity.
func generateSelfSignedCert(commonName string) (*tls.Certificate, error) {
	if commonName == "" {
		commonName = "localhost"
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		// A real TLS server presents a leaf (end-entity) certificate. This key is
		// ECDSA, so digitalSignature + serverAuth are sufficient; keyEncipherment
		// would describe an RSA-style key exchange and creates a fingerprint.
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(commonName); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{commonName}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	// Populate Leaf so callers can check NotAfter without re-parsing the DER.
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func (tcpStrategy *TCPStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	if tcpStrategy.Sessions == nil {
		tcpStrategy.Sessions = historystore.NewHistoryStore()
	}

	var tlsState *tlsCertificateState
	for _, cmd := range servConf.Commands {
		if cmd.TLSUpgrade {
			var err error
			tlsState, err = newTLSCertificateState(servConf)
			if err != nil {
				return fmt.Errorf("initializing TCP TLS certificate: %w", err)
			}
			break
		}
	}

	listen, err := net.Listen("tcp", servConf.Address)
	if err != nil {
		log.Errorf("Error during init TCP Protocol: %v", err)
		return err
	}

	acceptDone := make(chan struct{})
	tcpStrategy.lifecycleMu.Lock()
	if tcpStrategy.ctx == nil {
		tcpStrategy.ctx, tcpStrategy.cancel = context.WithCancel(context.Background())
	}
	strategyCtx := tcpStrategy.ctx
	tcpStrategy.listeners = append(tcpStrategy.listeners, listen)
	tcpStrategy.acceptDone = append(tcpStrategy.acceptDone, acceptDone)
	tcpStrategy.lifecycleMu.Unlock()
	tcpStrategy.activeMu.Lock()
	if tcpStrategy.activeConns == nil {
		tcpStrategy.activeConns = make(map[net.Conn]struct{})
	}
	tcpStrategy.activeMu.Unlock()

	tcpStrategy.cleanerOnce.Do(func() {
		tcpStrategy.Sessions.HistoryCleaner()
	})

	go func() {
		defer close(acceptDone)
		for {
			conn, err := listen.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				log.Errorf("Error accepting TCP connection: %v", err)
				continue
			}
			tcpStrategy.activeMu.Lock()
			tcpStrategy.activeConns[conn] = struct{}{}
			tcpStrategy.handlers.Add(1)
			tcpStrategy.activeMu.Unlock()
			go func(c net.Conn) {
				defer tcpStrategy.handlers.Done()
				defer func() {
					tcpStrategy.activeMu.Lock()
					delete(tcpStrategy.activeConns, c)
					tcpStrategy.activeMu.Unlock()
				}()
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("panic in TCP handler: %v", r)
					}
				}()
				handleTCPConnectionWithState(c, servConf, tr, tcpStrategy, tlsState, strategyCtx)
			}(conn)
		}
	}()

	log.WithFields(log.Fields{
		"port":     servConf.Address,
		"banner":   servConf.Banner,
		"commands": len(servConf.Commands),
	}).Infof("Init service %s", servConf.Protocol)
	return nil
}

// Shutdown stops listeners, active connections, command plugins, handler
// goroutines, and the session history cleaner owned by this strategy.
func (tcpStrategy *TCPStrategy) Shutdown() error {
	tcpStrategy.lifecycleMu.Lock()
	listeners := append([]net.Listener(nil), tcpStrategy.listeners...)
	acceptDone := append([]chan struct{}(nil), tcpStrategy.acceptDone...)
	cancel := tcpStrategy.cancel
	tcpStrategy.listeners = nil
	tcpStrategy.acceptDone = nil
	tcpStrategy.lifecycleMu.Unlock()

	var err error
	for _, listen := range listeners {
		if closeErr := listen.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}
	for _, done := range acceptDone {
		<-done
	}
	if cancel != nil {
		cancel()
	}
	tcpStrategy.activeMu.Lock()
	connections := make([]net.Conn, 0, len(tcpStrategy.activeConns))
	for conn := range tcpStrategy.activeConns {
		connections = append(connections, conn)
	}
	tcpStrategy.activeMu.Unlock()
	for _, conn := range connections {
		if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}
	tcpStrategy.handlers.Wait()
	if tcpStrategy.Sessions != nil {
		tcpStrategy.Sessions.Close()
	}
	return err
}

// tcpSession holds the immutable per-connection context shared by the helpers
// that handle one interactive TCP connection.
type tcpSession struct {
	servConf   parser.BeelzebubServiceConfiguration
	tr         tracer.Tracer
	strategy   *TCPStrategy
	tlsState   *tlsCertificateState
	ctx        context.Context
	host       string
	port       string
	remoteAddr string
	connID     string // per-connection UUID (WireContext.ConnID)
	sessionKey string // per-source key for cross-connection LLM history
	cutoff     time.Time
}

func tcpHistoryKey(servConf parser.BeelzebubServiceConfiguration, host string) string {
	return "TCP|" + servConf.Address + "|" + host
}

func handleTCPConnection(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, tcpStrategy *TCPStrategy) {
	var tlsState *tlsCertificateState
	for _, command := range servConf.Commands {
		if !command.TLSUpgrade {
			continue
		}
		var err error
		tlsState, err = newTLSCertificateState(servConf)
		if err != nil {
			log.Errorf("TLS certificate initialization failed: %v", err)
			conn.Close()
			return
		}
		break
	}
	handleTCPConnectionWithState(conn, servConf, tr, tcpStrategy, tlsState, context.Background())
}

func handleTCPConnectionWithState(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, tcpStrategy *TCPStrategy, tlsState *tlsCertificateState, ctx context.Context) {
	// Closure form (not `defer conn.Close()`): conn is reassigned to the
	// TLS-wrapped connection on upgrade, and the closure captures it by
	// reference, so this closes the encrypted connection, not the original.
	defer func() { conn.Close() }()

	// A non-positive timeout means "no deadline"; setting SetDeadline(now+0)
	// would pin the deadline to the current instant and break every read.
	cutoff := deadlineCutoff(time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second)
	if !cutoff.IsZero() {
		if err := conn.SetDeadline(cutoff); err != nil {
			log.Debugf("set connection deadline failed: %v", err)
		}
	}

	host, port, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		log.Debugf("cannot split remote address %q: %v", conn.RemoteAddr().String(), err)
		host = conn.RemoteAddr().String()
		port = ""
	}

	// Send banner if configured. Encoded via Latin-1 so binary (\xHH) banners
	// survive; trailing "\n" preserves upstream line-based banner behavior.
	if servConf.Banner != "" {
		// Preserve upstream behavior (banner + "\n") but binary-safe: the banner
		// may contain \xHH escapes, so encode via Latin-1 rather than %s.
		if err := writeAll(conn, append(staticResponseBytes(servConf.Banner, servConf.WireEncoding), '\n')); err != nil {
			log.Debugf("banner write failed: %v", err)
			return
		}
	}

	// Backward compatibility: if no commands configured, use legacy behavior.
	if len(servConf.Commands) == 0 {
		serveStateless(conn, servConf, tr, host, port)
		return
	}

	sess := &tcpSession{
		servConf:   servConf,
		tr:         tr,
		strategy:   tcpStrategy,
		tlsState:   tlsState,
		ctx:        ctx,
		host:       host,
		port:       port,
		remoteAddr: conn.RemoteAddr().String(),
		connID:     uuid.New().String(),
		sessionKey: tcpHistoryKey(servConf, host),
		cutoff:     cutoff,
	}
	sess.serve(conn)
}

// serveStateless handles the legacy no-commands path: read one buffer, emit a
// single stateless attempt event, and return.
func serveStateless(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, host, port string) {
	buffer := make([]byte, 1024)
	command := ""
	commandRaw := ""
	if n, _ := conn.Read(buffer); n > 0 {
		command = string(buffer[:n])
		if !utf8.Valid(buffer[:n]) {
			commandRaw = hexEscapeNonPrintable(buffer[:n])
		}
	}
	tr.TraceEvent(tracer.Event{
		Msg:         "New TCP attempt",
		Protocol:    tracer.TCP.String(),
		Command:     command,
		CommandRaw:  commandRaw,
		Status:      tracer.Stateless.String(),
		RemoteAddr:  conn.RemoteAddr().String(),
		SourceIp:    host,
		SourcePort:  port,
		ID:          uuid.New().String(),
		Description: servConf.Description,
	})
}

// serve runs the interactive command loop for one connection.
func (s *tcpSession) serve(conn net.Conn) {
	// Release any per-connection wire-plugin state when the connection ends, so
	// incomplete handshakes (e.g. a scanner that only sends a challenge request)
	// don't leak entries in plugin challenge stores. Keyed by ConnID (this
	// connection), not sessionKey (shared across connections from the same IP).
	defer func() {
		closeCtx := context.Background()
		if s.ctx != nil {
			closeCtx = context.WithoutCancel(s.ctx)
		}
		closeCtx, cancel := context.WithTimeout(closeCtx, 5*time.Second)
		defer cancel()
		closeWireSessions(closeCtx, s.connID, s.servConf.WirePlugins)
	}()

	s.tr.TraceEvent(tracer.Event{
		Msg:         "New TCP Session",
		Protocol:    tracer.TCP.String(),
		RemoteAddr:  s.remoteAddr,
		SourceIp:    s.host,
		SourcePort:  s.port,
		Status:      tracer.Start.String(),
		ID:          s.connID,
		Description: s.servConf.Description,
	})
	defer s.tr.TraceEvent(tracer.Event{
		Msg:      "End TCP Session",
		Status:   tracer.End.String(),
		ID:       s.connID,
		Protocol: tracer.TCP.String(),
	})

	histories := s.loadHistory()

	// Message reader: with a framing spec it reads one length-prefixed frame at
	// a time (split-read/pipeline safe); otherwise it accumulates opportunistically
	// until a handler can match. A fresh reader is rebuilt after a TLS upgrade so
	// it reads from the encrypted connection.
	currentFraming := s.servConf.Framing
	reader := &connReader{conn: conn, framing: currentFraming, wireEncoding: s.servConf.WireEncoding, cutoff: s.cutoff}

	for {
		rawBuffer, err := reader.nextMessage(s.servConf.Commands)
		if len(rawBuffer) == 0 {
			break
		}
		terminalReadError := err != nil
		if err != nil {
			log.Debugf("tcp read returned buffered data with terminal error: %v", err)
		}

		commandInput := commandMatchInput(rawBuffer, s.servConf.WireEncoding)

		// Preserve the exact bytes when the input is not valid UTF-8 (binary
		// protocols), so the forensic record survives the lossy Latin-1→UTF-8
		// re-encoding that Command undergoes during JSON serialization. Empty
		// for UTF-8/ASCII traffic.
		commandRaw := ""
		if !utf8.Valid(rawBuffer) {
			commandRaw = hexEscapeNonPrintable(rawBuffer)
		}

		matched := false
		for _, command := range s.servConf.Commands {
			if !command.Regex.MatchString(commandInput) {
				continue
			}
			matched = true

			ev, outputBytes, newEntries := s.handleMatch(command, rawBuffer, commandInput, commandRaw, histories)
			histories = append(histories, newEntries...)
			if maxHistory := s.maxHistoryEntries(); len(histories) > maxHistory {
				histories = histories[len(histories)-maxHistory:]
			}

			// Write the (possibly wire-plugin-modified) response.
			if len(outputBytes) > 0 {
				if err := writeAll(conn, outputBytes); err != nil {
					log.Debugf("TCP response write failed: %v", err)
					return
				}
			}

			s.tr.TraceEvent(ev)

			if command.TLSUpgrade {
				upgraded, ok := s.upgradeTLS(conn)
				if !ok {
					return
				}
				conn = upgraded // subsequent loop iterations read/write encrypted
				// Rebuild the reader on the encrypted connection; any bytes
				// buffered before the upgrade belong to the cleartext phase.
				if command.TLSFraming != nil {
					currentFraming = command.TLSFraming
				}
				reader = &connReader{conn: conn, framing: currentFraming, wireEncoding: s.servConf.WireEncoding, cutoff: s.cutoff}
			}

			if command.CloseAfter {
				return
			}
			break
		}

		if !matched {
			s.traceNotFound(commandInput, commandRaw)
		}
		if terminalReadError {
			return
		}
	}

}

// loadHistory loads the LLM context history for the session, capped to avoid
// context overflow. Each entry is a user+assistant pair, so 20 entries = 10
// exchanges. Configurable per service via maxHistory; defaults to 20.
func (s *tcpSession) loadHistory() []plugins.Message {
	maxHistoryEntries := s.maxHistoryEntries()
	if !s.strategy.Sessions.HasKey(s.sessionKey) {
		return nil
	}
	all := s.strategy.Sessions.Query(s.sessionKey)
	if len(all) > maxHistoryEntries {
		log.Debugf("session %s: history truncated from %d to %d entries", s.sessionKey, len(all), maxHistoryEntries)
		all = all[len(all)-maxHistoryEntries:]
	}
	return all
}

func (s *tcpSession) maxHistoryEntries() int {
	if s.servConf.MaxHistory > 0 {
		return s.servConf.MaxHistory
	}
	return 20
}

// handleMatch executes a matched command: it dispatches any plugin, builds the
// response bytes (Latin-1/patch or raw UTF-8), constructs the trace event, runs
// wire-plugins (which may rewrite the response and enrich the event), and
// persists the exchange to session history. It returns the event to emit, the
// response bytes to write, and the new history entries to append locally.
func (s *tcpSession) handleMatch(command parser.Command, rawBuffer []byte, commandInput, commandRaw string, histories []plugins.Message) (tracer.Event, []byte, []plugins.Message) {
	commandOutput := command.Handler
	handlerName := command.Name
	if handlerName == "" {
		handlerName = "configured_regex"
	}

	// Plugin dispatch via the registry. outputIsUTF8 means the output is already
	// real UTF-8 text (e.g. LLM output), so it must NOT be Latin-1 decoded or
	// byte-patched. A plugin can opt out by setting binaryOutput: true (its
	// output is then treated like a static handler: Latin-1 encoded + patched).
	// Static YAML handlers carry \xHH escapes parsed as Latin-1 codepoints and
	// always need latin1ToRawBytes + applyPatches.
	outputIsUTF8 := s.servConf.WireEncoding != "latin1"
	applyGenericPatches := command.Plugin == ""
	if command.Plugin != "" {
		if cp, ok := plugin.GetCommand(command.Plugin); ok {
			outputIsUTF8 = !command.BinaryOutput
			pluginCtx := s.ctx
			if pluginCtx == nil {
				pluginCtx = context.Background()
			}
			output, err := cp.Execute(pluginCtx, plugin.CommandRequest{
				Command:  commandInput,
				ClientIP: s.host,
				Protocol: "tcp",
				History:  plugins.MessagesToPlugin(histories),
				Config:   plugins.ConfigFromServiceConf(s.servConf),
			})
			if err != nil {
				log.Errorf("plugin %q execute error: %s", command.Plugin, err.Error())
				// Preserve a configured binary handler fallback when the plugin
				// fails, including its protocol patches.
				outputIsUTF8 = s.servConf.WireEncoding != "latin1"
				applyGenericPatches = true
			} else {
				commandOutput = output
				applyGenericPatches = command.BinaryOutput
			}
		} else {
			log.Warnf("unknown plugin %q, skipping", command.Plugin)
			outputIsUTF8 = s.servConf.WireEncoding != "latin1"
			applyGenericPatches = true
		}
	}

	// Build response bytes.
	var outputBytes []byte
	if commandOutput != "" {
		if outputIsUTF8 {
			outputBytes = []byte(commandOutput)
		} else {
			outputBytes = latin1ToRawBytes(commandOutput)
		}
	}
	if applyGenericPatches {
		outputBytes = applyPatches(outputBytes, command.Patches)
	}

	// Build the trace event (will be emitted after wire-plugins enrich it).
	ev := tracer.Event{
		Msg:           "TCP Session Interaction",
		RemoteAddr:    s.remoteAddr,
		SourceIp:      s.host,
		SourcePort:    s.port,
		Status:        tracer.Interaction.String(),
		Command:       commandInput,
		CommandRaw:    commandRaw,
		CommandOutput: commandOutput,
		ID:            s.connID,
		Protocol:      tracer.TCP.String(),
		Description:   s.servConf.Description,
		Handler:       handlerName,
	}

	// Wire-plugin dispatch: protocol-specific post-processing runs here through
	// the same public registry used by every externally installed plugin.
	wirePatches := make([]plugin.WirePatch, len(command.Patches))
	for i, patch := range command.Patches {
		wirePatches[i] = plugin.WirePatch{Type: patch.Type, Offset: patch.Offset, Length: patch.Length}
	}
	wireExchange := &plugin.WireContext{
		SessionKey:    s.sessionKey,
		ConnID:        s.connID,
		ClientIP:      s.host,
		ClientPort:    s.port,
		Request:       rawBuffer,
		Response:      outputBytes,
		CommandOutput: ev.CommandOutput,
		Metadata:      ev.Metadata,
		History:       plugins.MessagesToPlugin(histories),
		Command: plugin.WireCommand{
			Name:         command.Name,
			Handler:      command.Handler,
			Plugin:       command.Plugin,
			CloseAfter:   command.CloseAfter,
			TLSUpgrade:   command.TLSUpgrade,
			BinaryOutput: command.BinaryOutput,
			Patches:      wirePatches,
		},
		Service: plugin.WireService{
			Protocol:      s.servConf.Protocol,
			Address:       s.servConf.Address,
			Description:   s.servConf.Description,
			ServerName:    s.servConf.ServerName,
			ServerVersion: s.servConf.ServerVersion,
			WireEncoding:  s.servConf.WireEncoding,
			Config:        plugins.ConfigFromServiceConf(s.servConf),
		},
	}
	pluginCtx := s.ctx
	if pluginCtx == nil {
		pluginCtx = context.Background()
	}
	runWirePlugins(pluginCtx, s.servConf.WirePlugins, wireExchange)
	outputBytes = wireExchange.Response
	ev.CommandOutput = wireExchange.CommandOutput
	ev.Metadata = wireExchange.Metadata

	// Store command and response in history (after wire-plugins so any response
	// replacement they performed is captured).
	newEntries := []plugins.Message{
		{Role: plugins.USER.String(), Content: commandInput},
		{Role: plugins.ASSISTANT.String(), Content: ev.CommandOutput},
	}
	s.strategy.Sessions.AppendBounded(s.sessionKey, s.maxHistoryEntries(), newEntries...)

	return ev, outputBytes, newEntries
}

// upgradeTLS wraps conn in a TLS server using the strategy's configured or
// generated certificate
// and performs the handshake. It returns the encrypted connection and true on
// success, or (nil, false) if no cert is available or the handshake fails (the
// caller should then close the connection).
func (s *tcpSession) upgradeTLS(conn net.Conn) (net.Conn, bool) {
	cert := s.tlsState.current()
	if cert == nil {
		// Cert generation failed at Init: the connection would otherwise continue
		// in cleartext, silently defeating tlsUpgrade. Surface it and close.
		log.Warnf("tlsUpgrade requested but no TLS cert available; closing connection")
		return nil, false
	}
	tlsConn := tls.Server(conn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
	})
	if !s.cutoff.IsZero() {
		if err := conn.SetDeadline(s.cutoff); err != nil {
			log.Debugf("set TLS handshake deadline failed: %v", err)
		}
	}
	if err := tlsConn.Handshake(); err != nil {
		log.Debugf("TLS handshake: %v", err)
		return nil, false
	}
	return tlsConn, true
}

// traceNotFound emits the interaction event for input that matched no command.
func (s *tcpSession) traceNotFound(commandInput, commandRaw string) {
	s.tr.TraceEvent(tracer.Event{
		Msg:         "TCP Session Interaction",
		RemoteAddr:  s.remoteAddr,
		SourceIp:    s.host,
		SourcePort:  s.port,
		Status:      tracer.Interaction.String(),
		Command:     commandInput,
		CommandRaw:  commandRaw,
		ID:          s.connID,
		Protocol:    tracer.TCP.String(),
		Description: s.servConf.Description,
		Handler:     "not_found",
	})
}
