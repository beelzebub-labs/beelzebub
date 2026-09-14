package TELNET

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

const (
	IAC               = 255
	DO                = 253
	DONT              = 254
	WILL              = 251
	WONT              = 252
	SB                = 250
	SE                = 240
	ECHO              = 1
	SUPPRESS_GO_AHEAD = 3
)

type TelnetStrategy struct {
	Sessions    *historystore.HistoryStore
	listeners   map[string]net.Listener
	listenerWgs map[string]*sync.WaitGroup
	cleanerOnce sync.Once
}

var listenTCP = net.Listen

func (telnetStrategy *TelnetStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	if oldListener, ok := telnetStrategy.listeners[servConf.Address]; ok {
		oldListener.Close()
		if oldWg, ok := telnetStrategy.listenerWgs[servConf.Address]; ok {
			oldWg.Wait()
			delete(telnetStrategy.listenerWgs, servConf.Address)
		}
	}

	if telnetStrategy.Sessions == nil {
		telnetStrategy.Sessions = historystore.NewHistoryStore()
	}
	telnetStrategy.cleanerOnce.Do(func() {
		go telnetStrategy.Sessions.HistoryCleaner()
	})

	listener, err := listenTCP("tcp", servConf.Address)
	if err != nil {
		log.Errorf("error during init TELNET Protocol: %s", err.Error())
		return err
	}

	if telnetStrategy.listeners == nil {
		telnetStrategy.listeners = make(map[string]net.Listener)
	}
	if telnetStrategy.listenerWgs == nil {
		telnetStrategy.listenerWgs = make(map[string]*sync.WaitGroup)
	}
	telnetStrategy.listeners[servConf.Address] = listener

	listenerWg := &sync.WaitGroup{}
	listenerWg.Add(1)
	telnetStrategy.listenerWgs[servConf.Address] = listenerWg
	go func() {
		defer listenerWg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				log.Errorf("error accepting TELNET connection: %s", err.Error())
				continue
			}

			conn.SetDeadline(time.Now().Add(time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second))

			go func(c net.Conn) {
				defer func() {
					if r := recover(); r != nil {
						log.Errorf("panic in TELNET handler: %v", r)
					}
				}()
				handleTelnetConnection(c, servConf, tr, telnetStrategy)
			}(conn)
		}
	}()

	return nil
}

func (telnetStrategy *TelnetStrategy) StopAll() error {
	if telnetStrategy.Sessions != nil {
		telnetStrategy.Sessions.Close()
	}
	telnetStrategy.cleanerOnce = sync.Once{}

	var errs []error
	for _, listener := range telnetStrategy.listeners {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, listenerWg := range telnetStrategy.listenerWgs {
		listenerWg.Wait()
	}
	telnetStrategy.listeners = nil
	telnetStrategy.listenerWgs = nil
	if len(errs) > 0 {
		return fmt.Errorf("telnet stop errors: %w", errors.Join(errs...))
	}
	return nil
}

func (telnetStrategy *TelnetStrategy) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	listener, ok := telnetStrategy.listeners[servConf.Address]
	if !ok {
		return nil
	}
	if err := listener.Close(); err != nil {
		return err
	}
	if listenerWg, ok := telnetStrategy.listenerWgs[servConf.Address]; ok {
		listenerWg.Wait()
		delete(telnetStrategy.listenerWgs, servConf.Address)
	}
	delete(telnetStrategy.listeners, servConf.Address)
	return nil
}

func handleTelnetConnection(conn net.Conn, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, telnetStrategy *TelnetStrategy) {
	defer conn.Close()

	host, port, _ := net.SplitHostPort(conn.RemoteAddr().String())

	negotiateTelnet(conn)

	_, err := conn.Write([]byte("\r\nlogin: "))
	if err != nil {
		return
	}

	username, err := readLine(conn)
	if err != nil {
		return
	}
	username = strings.TrimSpace(username)

	_, err = conn.Write([]byte{IAC, WILL, ECHO})
	if err != nil {
		return
	}
	_, err = conn.Write([]byte("Password: "))
	if err != nil {
		return
	}

	password, err := readLine(conn)
	if err != nil {
		return
	}
	password = strings.TrimSpace(password)

	_, err = conn.Write([]byte{IAC, WONT, ECHO, '\r', '\n'})
	if err != nil {
		return
	}

	tr.TraceEvent(tracer.Event{
		Msg:         "New TELNET Login Attempt",
		Protocol:    tracer.TELNET.String(),
		Status:      tracer.Stateless.String(),
		User:        username,
		Password:    password,
		RemoteAddr:  conn.RemoteAddr().String(),
		SourceIp:    host,
		SourcePort:  port,
		ID:          uuid.New().String(),
		Description: servConf.Description,
	})

	matched, err := regexp.MatchString(servConf.PasswordRegex, password)
	if err != nil {
		log.Errorf("error regex: %s, %s", servConf.PasswordRegex, err.Error())
		conn.Write([]byte("Login incorrect\r\n"))
		return
	}

	if !matched {
		conn.Write([]byte("Login incorrect\r\n"))
		return
	}

	uuidSession := uuid.New()
	sessionKey := "TELNET" + host + username

	tr.TraceEvent(tracer.Event{
		Msg:         "New TELNET Terminal Session",
		Protocol:    tracer.TELNET.String(),
		RemoteAddr:  conn.RemoteAddr().String(),
		SourceIp:    host,
		SourcePort:  port,
		Status:      tracer.Start.String(),
		ID:          uuidSession.String(),
		User:        username,
		Description: servConf.Description,
	})

	var histories []plugins.Message
	if telnetStrategy.Sessions.HasKey(sessionKey) {
		histories = telnetStrategy.Sessions.Query(sessionKey)
	}

	for {
		prompt := buildPrompt(username, servConf.ServerName)
		_, err := conn.Write([]byte(prompt))
		if err != nil {
			break
		}

		commandInput, err := readLine(conn)
		if err != nil {
			break
		}
		commandInput = strings.TrimSpace(commandInput)

		if commandInput == "exit" {
			break
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
							Protocol: "telnet",
							History:  plugins.MessagesToPlugin(histories),
							Config:   plugins.ConfigFromServiceConf(servConf),
						})
						if err != nil {
							log.Errorf("plugin %q execute error: %s", command.Plugin, err.Error())
							commandOutput = "command not found"
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
				telnetStrategy.Sessions.Append(sessionKey, newEntries...)
				histories = append(histories, newEntries...)

				_, err := conn.Write([]byte(commandOutput + "\r\n"))
				if err != nil {
					break
				}

				tr.TraceEvent(tracer.Event{
					Msg:           "TELNET Terminal Session Interaction",
					RemoteAddr:    conn.RemoteAddr().String(),
					SourceIp:      host,
					SourcePort:    port,
					Status:        tracer.Interaction.String(),
					Command:       commandInput,
					CommandOutput: commandOutput,
					ID:            uuidSession.String(),
					Protocol:      tracer.TELNET.String(),
					User:          username,
					Description:   servConf.Description,
					Handler:       handlerName,
				})

				break
			}
		}

		if !matched {
			commandOutput := "command not found"
			_, err := conn.Write([]byte(commandOutput + "\r\n"))
			if err != nil {
				break
			}

			tr.TraceEvent(tracer.Event{
				Msg:           "TELNET Terminal Session Interaction",
				RemoteAddr:    conn.RemoteAddr().String(),
				SourceIp:      host,
				SourcePort:    port,
				Status:        tracer.Interaction.String(),
				Command:       commandInput,
				CommandOutput: commandOutput,
				ID:            uuidSession.String(),
				Protocol:      tracer.TELNET.String(),
				User:          username,
				Description:   servConf.Description,
				Handler:       "not_found",
			})
		}
	}

	tr.TraceEvent(tracer.Event{
		Msg:      "End TELNET Session",
		Status:   tracer.End.String(),
		ID:       uuidSession.String(),
		Protocol: tracer.TELNET.String(),
	})
}

func negotiateTelnet(conn net.Conn) {
	buf := make([]byte, 256)
	conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	conn.Read(buf)
	conn.SetReadDeadline(time.Time{})
}

func readLine(conn net.Conn) (string, error) {
	var line []byte
	buf := make([]byte, 1)

	for {
		_, err := conn.Read(buf)
		if err != nil {
			return "", err
		}

		b := buf[0]

		if b == IAC {
			if _, err := conn.Read(buf); err != nil {
				return "", err
			}
			cmd := buf[0]
			if cmd == SB {
				for {
					if _, err := conn.Read(buf); err != nil {
						return "", err
					}
					if buf[0] == IAC {
						if _, err := conn.Read(buf); err != nil {
							return "", err
						}
						if buf[0] == SE {
							break
						}
					}
				}
			} else if cmd == WILL || cmd == WONT || cmd == DO || cmd == DONT {
				conn.Read(buf)
			}
			continue
		}

		if b == '\n' {
			break
		}

		if b >= 32 && b <= 126 || b == '\t' {
			line = append(line, b)
		}
	}

	return string(line), nil
}

func buildPrompt(user string, serverName string) string {
	return fmt.Sprintf("%s@%s:~$ ", user, serverName)
}
