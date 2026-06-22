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
				rand.Read(out[p.Offset : p.Offset+p.Length])
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
	tlsCert  *tls.Certificate // generated at Init when any command has tlsUpgrade:true
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
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func (tcpStrategy *TCPStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	if tcpStrategy.Sessions == nil {
		tcpStrategy.Sessions = historystore.NewHistoryStore()
	}
	go tcpStrategy.Sessions.HistoryCleaner()

	for _, cmd := range servConf.Commands {
		if cmd.TLSUpgrade {
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

func handleTCPConnection(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, tcpStrategy *TCPStrategy) {
	defer func() { conn.Close() }() // closure captures conn by reference to support TLS upgrade

	conn.SetDeadline(time.Now().Add(time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second))

	host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())

	// Send banner if configured. Binary banners use Latin-1 encoding (no trailing newline).
	if servConf.Banner != "" {
		// Preserve upstream behavior (banner + "\n") but binary-safe: the banner
		// may contain \xHH escapes, so encode via Latin-1 rather than %s.
		conn.Write(append(latin1ToRawBytes(servConf.Banner), '\n'))
	}

	// Backward compatibility: if no commands configured, use legacy behavior
	if len(servConf.Commands) == 0 {
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
		return
	}

	// Interactive session mode
	sessionID := uuid.New()
	sessionKey := "TCP" + host

	tr.TraceEvent(tracer.Event{
		Msg:         "New TCP Session",
		Protocol:    tracer.TCP.String(),
		RemoteAddr:  conn.RemoteAddr().String(),
		SourceIp:    host,
		SourcePort:  port,
		Status:      tracer.Start.String(),
		ID:          sessionID.String(),
		Description: servConf.Description,
	})

	// Load history for LLM context, capped to avoid context overflow.
	// Each entry is a user+assistant pair, so 20 entries = 10 exchanges.
	const maxHistoryEntries = 20
	var histories []plugins.Message
	if tcpStrategy.Sessions.HasKey(sessionKey) {
		all := tcpStrategy.Sessions.Query(sessionKey)
		if len(all) > maxHistoryEntries {
			all = all[len(all)-maxHistoryEntries:]
		}
		histories = all
	}

	// Interactive command loop
	for {
		buffer := make([]byte, 4096)
		n, err := conn.Read(buffer)
		if err != nil {
			break
		}

		rawBuffer := buffer[:n]
		commandInput := strings.TrimRight(rawBytesToLatin1(rawBuffer), "\r\n")

		// Preserve the exact bytes when the input is not valid UTF-8 (binary
		// protocols), so the forensic record survives the lossy Latin-1→UTF-8
		// re-encoding that Command undergoes during JSON serialization. Empty
		// for UTF-8/ASCII traffic.
		commandRaw := ""
		if !utf8.Valid(rawBuffer) {
			commandRaw = hexEscapeNonPrintable(rawBuffer)
		}

		// Match command against regexes
		matched := false
		for _, command := range servConf.Commands {
			if command.Regex.MatchString(commandInput) {
				matched = true
				commandOutput := command.Handler
				handlerName := command.Name
				if handlerName == "" {
					handlerName = "configured_regex"
				}

				// Plugin dispatch via the registry.
				isLLMResponse := false
				if command.Plugin != "" {
					if cp, ok := plugin.GetCommand(command.Plugin); ok {
						isLLMResponse = true
						output, err := cp.Execute(context.Background(), plugin.CommandRequest{
							Command:  commandInput,
							ClientIP: host,
							Protocol: "tcp",
							History:  plugins.MessagesToPlugin(histories),
							Config:   plugins.ConfigFromServiceConf(servConf),
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
				// LLM responses are real UTF-8 text — send as-is.
				// Static YAML handlers use \xHH escapes parsed as Latin-1 codepoints
				// by the YAML library — convert back to raw bytes before writing.
				var outputBytes []byte
				if commandOutput != "" {
					if isLLMResponse {
						outputBytes = []byte(commandOutput)
					} else {
						outputBytes = latin1ToRawBytes(commandOutput)
					}
				}
				if !isLLMResponse {
					outputBytes = applyPatches(outputBytes, command.Patches)
				}

				// Build the trace event (will be emitted after wire-plugins enrich it).
				ev := tracer.Event{
					Msg:           "TCP Session Interaction",
					RemoteAddr:    conn.RemoteAddr().String(),
					SourceIp:      host,
					SourcePort:    port,
					Status:        tracer.Interaction.String(),
					Command:       commandInput,
					CommandRaw:    commandRaw,
					CommandOutput: commandOutput,
					ID:            sessionID.String(),
					Protocol:      tracer.TCP.String(),
					Description:   servConf.Description,
					Handler:       handlerName,
				}

				// Wire-plugin dispatch: protocol-specific post-processing runs
				// here. Wire-plugins may modify outputBytes and enrich ev
				// (e.g. via ev.Metadata). Empty registry → no-op.
				runWirePlugins(&WireContext{
					SessionKey:    sessionKey,
					Command:       &command,
					Request:       rawBuffer,
					Response:      &outputBytes,
					Event:         &ev,
					Histories:     histories,
					ServiceConfig: servConf,
				})

				// Store command and response in history (after wire-plugins so
				// any response replacement they performed is captured).
				var newEntries []plugins.Message
				newEntries = append(newEntries, plugins.Message{Role: plugins.USER.String(), Content: commandInput})
				newEntries = append(newEntries, plugins.Message{Role: plugins.ASSISTANT.String(), Content: ev.CommandOutput})
				tcpStrategy.Sessions.Append(sessionKey, newEntries...)
				histories = append(histories, newEntries...)

				// Write the (possibly wire-plugin-modified) response.
				if len(outputBytes) > 0 {
					if _, err := conn.Write(outputBytes); err != nil {
						break
					}
				}

				tr.TraceEvent(ev)

				if command.TLSUpgrade && tcpStrategy.tlsCert != nil {
					tlsConn := tls.Server(conn, &tls.Config{
						Certificates: []tls.Certificate{*tcpStrategy.tlsCert},
						MinVersion:   tls.VersionTLS12,
					})
					conn.SetDeadline(time.Now().Add(time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second))
					if err := tlsConn.Handshake(); err != nil {
						log.Debugf("TLS handshake: %v", err)
						return
					}
					conn = tlsConn // subsequent loop iterations read/write encrypted
				}

				if command.CloseAfter {
					return
				}
				break
			}
		}

		// If no command matched
		if !matched {
			tr.TraceEvent(tracer.Event{
				Msg:         "TCP Session Interaction",
				RemoteAddr:  conn.RemoteAddr().String(),
				SourceIp:    host,
				SourcePort:  port,
				Status:      tracer.Interaction.String(),
				Command:     commandInput,
				CommandRaw:  commandRaw,
				ID:          sessionID.String(),
				Protocol:    tracer.TCP.String(),
				Description: servConf.Description,
				Handler:     "not_found",
			})
		}
	}

	// Trace session end
	tr.TraceEvent(tracer.Event{
		Msg:      "End TCP Session",
		Status:   tracer.End.String(),
		ID:       sessionID.String(),
		Protocol: tracer.TCP.String(),
	})
}
