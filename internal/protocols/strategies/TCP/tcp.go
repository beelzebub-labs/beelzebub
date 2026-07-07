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
	"fmt"
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

// hexEscapeNonPrintable renders b as printable ASCII, emitting every
// non-printable byte (and the backslash itself) as \xNN. The result is safe to
// store in trace events, JSON, and TEXT columns while remaining reversible to
// the original bytes. The \xNN form matches the escape syntax already used in
// handler/regex fields of the service YAML, so logs read consistently with
// configuration.
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

// applyPatches applies the generic Patches declared on a Command to buf and
// returns the patched copy. Supported patch types are:
//
//   - "random"   — write Length cryptographically random bytes at Offset
//   - "filetime" — write 8-byte Windows FILETIME (current UTC) at Offset
//
// Any other patch type is ignored here and left for wire-plugins to interpret.
// deadlineCutoff returns the absolute deadline for a connection given the
// configured timeout, or the zero time (no deadline) when the timeout is <= 0.
func deadlineCutoff(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d)
}

func applyPatches(buf []byte, patches []parser.Patch) []byte {
	if len(patches) == 0 {
		return buf
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	for _, p := range patches {
		switch p.Type {
		case "random":
			if p.Length > 0 && p.Offset >= 0 && p.Offset+p.Length <= len(out) {
				if _, err := rand.Read(out[p.Offset : p.Offset+p.Length]); err != nil {
					// crypto/rand failure is effectively fatal for the host; leave
					// the original bytes rather than emitting predictable zeros.
					log.Errorf("random patch: crypto/rand read failed: %v", err)
				}
			}
		case "filetime":
			if p.Offset >= 0 && p.Offset+8 <= len(out) {
				copy(out[p.Offset:], toWindowsFileTime(time.Now()))
			}
		}
	}
	return out
}

type TCPStrategy struct {
	Sessions *historystore.HistoryStore
	tlsMu    sync.Mutex
	tlsCert  *tls.Certificate // generated at Init when any command has tlsUpgrade:true
	tlsCN    string           // common name used to (re)generate tlsCert
}

// currentTLSCert returns the self-signed cert for TLS upgrade, lazily
// regenerating it if it has expired (a honeypot may run longer than the cert's
// validity). Returns nil if no cert is available and regeneration fails.
func (tcpStrategy *TCPStrategy) currentTLSCert() *tls.Certificate {
	tcpStrategy.tlsMu.Lock()
	defer tcpStrategy.tlsMu.Unlock()
	if tcpStrategy.tlsCert == nil {
		return nil
	}
	if leaf := tcpStrategy.tlsCert.Leaf; leaf != nil && time.Now().After(leaf.NotAfter) {
		if cert, err := generateSelfSignedCert(tcpStrategy.tlsCN); err != nil {
			log.Errorf("TLS cert regeneration failed: %v", err)
		} else {
			tcpStrategy.tlsCert = cert
		}
	}
	return tcpStrategy.tlsCert
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
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     []string{commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		// A real TLS server presents a leaf (end-entity) certificate: key usage
		// digitalSignature + keyEncipherment, extKeyUsage serverAuth, and NOT a
		// CA. Emitting IsCA/keyCertSign here gave fingerprinting tools a tell.
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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
	go tcpStrategy.Sessions.HistoryCleaner()

	for _, cmd := range servConf.Commands {
		if cmd.TLSUpgrade {
			tcpStrategy.tlsCN = servConf.ServerName
			cert, err := generateSelfSignedCert(servConf.ServerName)
			if err != nil {
				log.Errorf("TLS cert generation failed: %v", err)
			} else {
				tcpStrategy.tlsCert = cert
			}
			break
		}
	}

	listen, err := net.Listen("tcp", servConf.Address)
	if err != nil {
		log.Errorf("Error during init TCP Protocol: %s", err.Error())
		return err
	}

	go func() {
		for {
			if conn, err := listen.Accept(); err == nil {
				go func(c net.Conn) {
					defer func() {
						if r := recover(); r != nil {
							log.Errorf("panic in TCP handler: %v", r)
						}
					}()
					handleTCPConnection(c, servConf, tr, tcpStrategy)
				}(conn)
			}
		}
	}()

	log.WithFields(log.Fields{
		"port":     servConf.Address,
		"banner":   servConf.Banner,
		"commands": len(servConf.Commands),
	}).Infof("Init service %s", servConf.Protocol)
	return nil
}

// tcpSession holds the immutable per-connection context shared by the helpers
// that handle one interactive TCP connection.
type tcpSession struct {
	servConf   parser.BeelzebubServiceConfiguration
	tr         tracer.Tracer
	strategy   *TCPStrategy
	host       string
	port       string
	remoteAddr string
	connID     string // per-connection UUID (WireContext.ConnID)
	sessionKey string // per-source key for cross-connection LLM history
}

func handleTCPConnection(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, tcpStrategy *TCPStrategy) {
	// Closure form (not `defer conn.Close()`): conn is reassigned to the
	// TLS-wrapped connection on upgrade, and the closure captures it by
	// reference, so this closes the encrypted connection, not the original.
	defer func() { conn.Close() }()

	// A non-positive timeout means "no deadline"; setting SetDeadline(now+0)
	// would pin the deadline to the current instant and break every read.
	if d := time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second; d > 0 {
		conn.SetDeadline(time.Now().Add(d))
	}

	host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())

	// Send banner if configured. Encoded via Latin-1 so binary (\xHH) banners
	// survive; trailing "\n" preserves upstream line-based banner behavior.
	if servConf.Banner != "" {
		// Preserve upstream behavior (banner + "\n") but binary-safe: the banner
		// may contain \xHH escapes, so encode via Latin-1 rather than %s.
		conn.Write(append(latin1ToRawBytes(servConf.Banner), '\n'))
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
		host:       host,
		port:       port,
		remoteAddr: conn.RemoteAddr().String(),
		connID:     uuid.New().String(),
		sessionKey: "TCP" + host,
	}
	sess.serve(conn)
}

// serveStateless handles the legacy no-commands path: read one buffer, emit a
// single stateless attempt event, and return.
func serveStateless(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, host, port string) {
	buffer := make([]byte, 1024)
	command := ""
	commandRaw := ""
	if n, err := conn.Read(buffer); err == nil {
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
	defer closeWireSessions(s.connID, s.servConf.WirePlugins)

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

	histories := s.loadHistory()

	// Message reader: with a framing spec it reads one length-prefixed frame at
	// a time (split-read/pipeline safe); otherwise it accumulates opportunistically
	// until a handler can match. A fresh reader is rebuilt after a TLS upgrade so
	// it reads from the encrypted connection.
	deadline := time.Duration(s.servConf.DeadlineTimeoutSeconds) * time.Second
	reader := &connReader{conn: conn, framing: s.servConf.Framing, cutoff: deadlineCutoff(deadline)}

	for {
		rawBuffer, err := reader.nextMessage(s.servConf.Commands)
		if err != nil || len(rawBuffer) == 0 {
			break
		}

		commandInput := strings.TrimRight(rawBytesToLatin1(rawBuffer), "\r\n")

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

			// Write the (possibly wire-plugin-modified) response.
			if len(outputBytes) > 0 {
				if _, err := conn.Write(outputBytes); err != nil {
					break
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
				reader = &connReader{conn: conn, framing: s.servConf.Framing, cutoff: deadlineCutoff(deadline)}
			}

			if command.CloseAfter {
				return
			}
			break
		}

		if !matched {
			s.traceNotFound(commandInput, commandRaw)
		}
	}

	s.tr.TraceEvent(tracer.Event{
		Msg:      "End TCP Session",
		Status:   tracer.End.String(),
		ID:       s.connID,
		Protocol: tracer.TCP.String(),
	})
}

// loadHistory loads the LLM context history for the session, capped to avoid
// context overflow. Each entry is a user+assistant pair, so 20 entries = 10
// exchanges. Configurable per service via maxHistory; defaults to 20.
func (s *tcpSession) loadHistory() []plugins.Message {
	maxHistoryEntries := s.servConf.MaxHistory
	if maxHistoryEntries <= 0 {
		maxHistoryEntries = 20
	}
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
	outputIsUTF8 := false
	if command.Plugin != "" {
		if cp, ok := plugin.GetCommand(command.Plugin); ok {
			outputIsUTF8 = !command.BinaryOutput
			output, err := cp.Execute(context.Background(), plugin.CommandRequest{
				Command:  commandInput,
				ClientIP: s.host,
				Protocol: "tcp",
				History:  plugins.MessagesToPlugin(histories),
				Config:   plugins.ConfigFromServiceConf(s.servConf),
			})
			if err != nil {
				log.Errorf("plugin %q execute error: %s", command.Plugin, err.Error())
			} else {
				commandOutput = output
			}
		} else {
			log.Warnf("unknown plugin %q, skipping", command.Plugin)
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
	if !outputIsUTF8 {
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

	// Wire-plugin dispatch: protocol-specific post-processing runs here.
	// Wire-plugins may modify outputBytes and enrich ev (e.g. via ev.Metadata).
	// Empty registry → no-op.
	runWirePlugins(&WireContext{
		SessionKey:    s.sessionKey,
		ConnID:        s.connID,
		Command:       &command,
		Request:       rawBuffer,
		Response:      &outputBytes,
		Event:         &ev,
		Histories:     histories,
		ServiceConfig: s.servConf,
	})

	// Store command and response in history (after wire-plugins so any response
	// replacement they performed is captured).
	newEntries := []plugins.Message{
		{Role: plugins.USER.String(), Content: commandInput},
		{Role: plugins.ASSISTANT.String(), Content: ev.CommandOutput},
	}
	s.strategy.Sessions.Append(s.sessionKey, newEntries...)

	return ev, outputBytes, newEntries
}

// upgradeTLS wraps conn in a TLS server using the strategy's self-signed cert
// and performs the handshake. It returns the encrypted connection and true on
// success, or (nil, false) if no cert is available or the handshake fails (the
// caller should then close the connection).
func (s *tcpSession) upgradeTLS(conn net.Conn) (net.Conn, bool) {
	cert := s.strategy.currentTLSCert()
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
	if d := time.Duration(s.servConf.DeadlineTimeoutSeconds) * time.Second; d > 0 {
		conn.SetDeadline(time.Now().Add(d))
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
