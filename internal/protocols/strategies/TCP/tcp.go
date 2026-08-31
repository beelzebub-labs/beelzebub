package TCP

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/beelzebub-labs/beelzebub/v3/internal"
	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

type TCPStrategy struct {
	Sessions *historystore.HistoryStore
}

func (tcpStrategy *TCPStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	if tcpStrategy.Sessions == nil {
		tcpStrategy.Sessions = historystore.NewHistoryStore()
	}
	go tcpStrategy.Sessions.HistoryCleaner()

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
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second))

	host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())

	// Send banner if configured
	if servConf.Banner != "" {
		conn.Write(fmt.Appendf([]byte{}, "%s\n", servConf.Banner))
	}

	// Backward compatibility: if no commands configured, use legacy behavior
	if len(servConf.Commands) == 0 {
		buffer := make([]byte, 1024)
		command := ""
		commandRaw := ""

		if n, err := conn.Read(buffer); err == nil {
			if !utf8.Valid(buffer[:n]) {
				commandRaw = hexEscapeNonPrintable(buffer[:n])
			}
			command = internal.PlainOrBase64(buffer[:n])
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

	// Load history for LLM context
	var histories []plugins.Message
	if tcpStrategy.Sessions.HasKey(sessionKey) {
		histories = tcpStrategy.Sessions.Query(sessionKey)
	}

	// Interactive command loop
	for {
		buffer := make([]byte, 4096)
		n, err := conn.Read(buffer)
		if err != nil {
			break
		}

		commandInput := strings.TrimRight(string(buffer[:n]), "\r\n")

		// Preserve the exact bytes when they are not valid UTF-8 (binary
		// protocols), so the forensic record survives string()'s U+FFFD
		// substitution. Empty for UTF-8 traffic.
		commandRaw := ""
		if !utf8.Valid(buffer[:n]) {
			commandRaw = hexEscapeNonPrintable(buffer[:n])
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

				// Plugin dispatch via registry
				if command.Plugin != "" {
					if cp, ok := plugin.GetCommand(command.Plugin); ok {
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

				// Store command and response in history
				var newEntries []plugins.Message
				newEntries = append(newEntries, plugins.Message{Role: plugins.USER.String(), Content: commandInput})
				newEntries = append(newEntries, plugins.Message{Role: plugins.ASSISTANT.String(), Content: commandOutput})
				tcpStrategy.Sessions.Append(sessionKey, newEntries...)
				histories = append(histories, newEntries...)

				// Send response to client
				if commandOutput != "" {
					_, err := conn.Write([]byte(commandOutput))
					if err != nil {
						break
					}
				}

				// Trace interaction event
				tr.TraceEvent(tracer.Event{
					Msg:           "TCP Session Interaction",
					RemoteAddr:    conn.RemoteAddr().String(),
					SourceIp:      host,
					SourcePort:    port,
					Status:        tracer.Interaction.String(),
					Command:       internal.PlainOrBase64([]byte(commandInput)),
					CommandRaw:    commandRaw,
					CommandOutput: commandOutput,
					ID:            sessionID.String(),
					Protocol:      tracer.TCP.String(),
					Description:   servConf.Description,
					Handler:       handlerName,
				})

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
				Command:     internal.PlainOrBase64([]byte(commandInput)),
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

// hexEscapeNonPrintable renders b as printable ASCII, emitting every
// non-printable byte (and the backslash itself) as \xNN. The result is safe to
// store in trace events, JSON, and TEXT columns while remaining reversible to
// the original bytes.
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
