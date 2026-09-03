package TCP

import (
	"context"
	"errors"
	"fmt"
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

type TCPStrategy struct {
	Sessions    *historystore.HistoryStore
	listeners   map[string]net.Listener
	listenerWgs map[string]*sync.WaitGroup
	cleanerOnce sync.Once
}

var listenTCP = net.Listen

func (tcpStrategy *TCPStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	if oldListener, ok := tcpStrategy.listeners[servConf.Address]; ok {
		oldListener.Close()
		if oldWg, ok := tcpStrategy.listenerWgs[servConf.Address]; ok {
			oldWg.Wait()
			delete(tcpStrategy.listenerWgs, servConf.Address)
		}
	}

	if tcpStrategy.Sessions == nil {
		tcpStrategy.Sessions = historystore.NewHistoryStore()
	}
	tcpStrategy.cleanerOnce.Do(func() {
		go tcpStrategy.Sessions.HistoryCleaner()
	})

	listen, err := listenTCP("tcp", servConf.Address)
	if err != nil {
		log.Errorf("Error during init TCP Protocol: %s", err.Error())
		return err
	}

	if tcpStrategy.listeners == nil {
		tcpStrategy.listeners = make(map[string]net.Listener)
	}
	if tcpStrategy.listenerWgs == nil {
		tcpStrategy.listenerWgs = make(map[string]*sync.WaitGroup)
	}
	tcpStrategy.listeners[servConf.Address] = listen

	listenerWg := &sync.WaitGroup{}
	listenerWg.Add(1)
	tcpStrategy.listenerWgs[servConf.Address] = listenerWg
	go func() {
		defer listenerWg.Done()
		for {
			conn, err := listen.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				log.Errorf("error accepting TCP connection: %s", err.Error())
				continue
			}
			go func(c net.Conn) {
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("panic in TCP handler: %v", r)
					}
				}()
				handleTCPConnection(c, servConf, tr, tcpStrategy)
			}(conn)
		}
	}()

	return nil
}

func (tcpStrategy *TCPStrategy) StopAll() error {
	if tcpStrategy.Sessions != nil {
		tcpStrategy.Sessions.Close()
	}
	tcpStrategy.cleanerOnce = sync.Once{}

	var errs []error
	for _, listener := range tcpStrategy.listeners {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, listenerWg := range tcpStrategy.listenerWgs {
		listenerWg.Wait()
	}
	tcpStrategy.listeners = nil
	tcpStrategy.listenerWgs = nil
	if len(errs) > 0 {
		return fmt.Errorf("tcp stop errors: %w", errors.Join(errs...))
	}
	return nil
}

func (tcpStrategy *TCPStrategy) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	listener, ok := tcpStrategy.listeners[servConf.Address]
	if !ok {
		return nil
	}
	if err := listener.Close(); err != nil {
		return err
	}
	if listenerWg, ok := tcpStrategy.listenerWgs[servConf.Address]; ok {
		listenerWg.Wait()
		delete(tcpStrategy.listenerWgs, servConf.Address)
	}
	delete(tcpStrategy.listeners, servConf.Address)
	return nil
}

func handleTCPConnection(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, tcpStrategy *TCPStrategy) {
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second))

	host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())

	if servConf.Banner != "" {
		conn.Write(fmt.Appendf([]byte{}, "%s\n", servConf.Banner))
	}

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

	var histories []plugins.Message
	if tcpStrategy.Sessions.HasKey(sessionKey) {
		histories = tcpStrategy.Sessions.Query(sessionKey)
	}

	for {
		buffer := make([]byte, 4096)
		n, err := conn.Read(buffer)
		if err != nil {
			break
		}

		commandInput := strings.TrimRight(string(buffer[:n]), "\r\n")

		commandRaw := ""
		if !utf8.Valid(buffer[:n]) {
			commandRaw = hexEscapeNonPrintable(buffer[:n])
		}

		matched := false
		for _, command := range servConf.Commands {
			if command.Regex.MatchString(commandInput) {
				matched = true
				commandOutput := command.Handler
				handlerName := command.Name
				if handlerName == "" {
					handlerName = "configured_regex"
				}

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

				var newEntries []plugins.Message
				newEntries = append(newEntries, plugins.Message{Role: plugins.USER.String(), Content: commandInput})
				newEntries = append(newEntries, plugins.Message{Role: plugins.ASSISTANT.String(), Content: commandOutput})
				tcpStrategy.Sessions.Append(sessionKey, newEntries...)
				histories = append(histories, newEntries...)

				if commandOutput != "" {
					_, err := conn.Write([]byte(commandOutput))
					if err != nil {
						break
					}
				}

				tr.TraceEvent(tracer.Event{
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
				})

				break
			}
		}

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

	tr.TraceEvent(tracer.Event{
		Msg:      "End TCP Session",
		Status:   tracer.End.String(),
		ID:       sessionID.String(),
		Protocol: tracer.TCP.String(),
	})
}

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
