package SSH

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	"github.com/gliderlabs/ssh"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"golang.org/x/term"
)

type SSHStrategy struct {
	Sessions    *historystore.HistoryStore
	servers     map[string]*ssh.Server
	serverReady map[string]<-chan struct{}
	serversMu   sync.RWMutex
	cleanerOnce sync.Once
}

type readyListener struct {
	net.Listener
	ready     chan struct{}
	readyOnce *sync.Once
}

func (l *readyListener) Accept() (net.Conn, error) {
	l.readyOnce.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}

func (sshStrategy *SSHStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	sshStrategy.serversMu.RLock()
	oldServer, ok := sshStrategy.servers[servConf.Address]
	sshStrategy.serversMu.RUnlock()
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		oldServer.Shutdown(ctx)
		cancel()
	}

	if sshStrategy.Sessions == nil {
		sshStrategy.Sessions = historystore.NewHistoryStore()
	}
	sshStrategy.cleanerOnce.Do(func() {
		go sshStrategy.Sessions.HistoryCleaner()
	})

	server := &ssh.Server{
		Addr:        servConf.Address,
		MaxTimeout:  time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second,
		IdleTimeout: time.Duration(servConf.DeadlineTimeoutSeconds) * time.Second,
		Version:     servConf.ServerVersion,
		Handler: func(sess ssh.Session) {
			handleSession(sess, servConf, tr, sshStrategy.Sessions)
		},
		PasswordHandler: func(ctx ssh.Context, password string) bool {
			return handlePassword(ctx, password, servConf, tr)
		},
	}

	ln, err := net.Listen("tcp", servConf.Address)
	if err != nil {
		return err
	}
	ready := make(chan struct{}, 1)
	readyOnce := &sync.Once{}
	server.Addr = ln.Addr().String()

	sshStrategy.serversMu.Lock()
	if sshStrategy.servers == nil {
		sshStrategy.servers = make(map[string]*ssh.Server)
	}
	if sshStrategy.serverReady == nil {
		sshStrategy.serverReady = make(map[string]<-chan struct{})
	}
	sshStrategy.servers[servConf.Address] = server
	sshStrategy.serverReady[servConf.Address] = ready
	sshStrategy.serversMu.Unlock()

	go func() {
		defer readyOnce.Do(func() { close(ready) })
		err := server.Serve(&readyListener{Listener: ln, ready: ready, readyOnce: readyOnce})
		if err != nil {
			if errors.Is(err, ssh.ErrServerClosed) {
				log.Debugf("SSH server on %s stopped: %s", servConf.Address, err.Error())
			} else {
				log.Errorf("error during init SSH Protocol: %s", err.Error())
			}
		}
	}()
	return nil
}

func handleSession(sess ssh.Session, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, sessions *historystore.HistoryStore) {
	uuidSession := uuid.New()

	host, port, _ := net.SplitHostPort(sess.RemoteAddr().String())
	sessionKey := "SSH" + host + sess.User()

	if sess.RawCommand() != "" {
		var histories []plugins.Message
		if sessions.HasKey(sessionKey) {
			histories = sessions.Query(sessionKey)
		}
		for _, command := range servConf.Commands {
			if command.Regex.MatchString(sess.RawCommand()) {
				commandOutput := command.Handler
				if command.Plugin != "" {
					if cp, ok := plugin.GetCommand(command.Plugin); ok {
						output, err := cp.Execute(context.Background(), plugin.CommandRequest{
							Command:  sess.RawCommand(),
							ClientIP: host,
							Protocol: "ssh",
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
				newEntries = append(newEntries, plugins.Message{Role: plugins.USER.String(), Content: sess.RawCommand()})
				newEntries = append(newEntries, plugins.Message{Role: plugins.ASSISTANT.String(), Content: commandOutput})
				sessions.Append(sessionKey, newEntries...)

				sess.Write(append([]byte(commandOutput), '\n'))

				tr.TraceEvent(tracer.Event{
					Msg:           "SSH Raw Command",
					Protocol:      tracer.SSH.String(),
					RemoteAddr:    sess.RemoteAddr().String(),
					SourceIp:      host,
					SourcePort:    port,
					Status:        tracer.Start.String(),
					ID:            uuidSession.String(),
					Environ:       strings.Join(sess.Environ(), ","),
					User:          sess.User(),
					Description:   servConf.Description,
					Command:       sess.RawCommand(),
					CommandOutput: commandOutput,
					Handler:       command.Name,
				})
				return
			}
		}
	}

	tr.TraceEvent(tracer.Event{
		Msg:         "New SSH Terminal Session",
		Protocol:    tracer.SSH.String(),
		RemoteAddr:  sess.RemoteAddr().String(),
		SourceIp:    host,
		SourcePort:  port,
		Status:      tracer.Start.String(),
		ID:          uuidSession.String(),
		Environ:     strings.Join(sess.Environ(), ","),
		User:        sess.User(),
		Description: servConf.Description,
	})

	terminal := term.NewTerminal(sess, buildPrompt(sess.User(), servConf.ServerName))
	var histories []plugins.Message
	if sessions.HasKey(sessionKey) {
		histories = sessions.Query(sessionKey)
	}

	for {
		commandInput, err := terminal.ReadLine()
		if err != nil {
			break
		}
		if commandInput == "exit" {
			break
		}
		for _, command := range servConf.Commands {
			if command.Regex.MatchString(commandInput) {
				commandOutput := command.Handler
				if command.Plugin != "" {
					if cp, ok := plugin.GetCommand(command.Plugin); ok {
						output, err := cp.Execute(context.Background(), plugin.CommandRequest{
							Command:  commandInput,
							ClientIP: host,
							Protocol: "ssh",
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
				sessions.Append(sessionKey, newEntries...)
				histories = append(histories, newEntries...)

				terminal.Write(append([]byte(commandOutput), '\n'))

				tr.TraceEvent(tracer.Event{
					Msg:           "SSH Terminal Session Interaction",
					RemoteAddr:    sess.RemoteAddr().String(),
					SourceIp:      host,
					SourcePort:    port,
					Status:        tracer.Interaction.String(),
					Command:       commandInput,
					CommandOutput: commandOutput,
					ID:            uuidSession.String(),
					Protocol:      tracer.SSH.String(),
					Description:   servConf.Description,
					Handler:       command.Name,
				})
				break
			}
		}
	}

	tr.TraceEvent(tracer.Event{
		Msg:      "End SSH Session",
		Status:   tracer.End.String(),
		ID:       uuidSession.String(),
		Protocol: tracer.SSH.String(),
	})
}

func handlePassword(ctx ssh.Context, password string, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) bool {
	host, port, _ := net.SplitHostPort(ctx.RemoteAddr().String())

	tr.TraceEvent(tracer.Event{
		Msg:         "New SSH Login Attempt",
		Protocol:    tracer.SSH.String(),
		Status:      tracer.Stateless.String(),
		User:        ctx.User(),
		Password:    password,
		Client:      ctx.ClientVersion(),
		RemoteAddr:  ctx.RemoteAddr().String(),
		SourceIp:    host,
		SourcePort:  port,
		ID:          uuid.New().String(),
		Description: servConf.Description,
	})
	matched, err := regexp.MatchString(servConf.PasswordRegex, password)
	if err != nil {
		log.Errorf("error matching password regex: %s", err.Error())
		return false
	}
	return matched
}

func (sshStrategy *SSHStrategy) StopAll() error {
	if sshStrategy.Sessions != nil {
		sshStrategy.Sessions.Close()
	}
	sshStrategy.cleanerOnce = sync.Once{}

	sshStrategy.serversMu.RLock()
	servers := make(map[string]*ssh.Server, len(sshStrategy.servers))
	readies := make(map[string]<-chan struct{}, len(sshStrategy.serverReady))
	for address, server := range sshStrategy.servers {
		servers[address] = server
	}
	for address, ready := range sshStrategy.serverReady {
		readies[address] = ready
	}
	sshStrategy.serversMu.RUnlock()

	var errs []error
	for address, server := range servers {
		waitForSSHServer(readies[address])
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := server.Shutdown(ctx); err != nil {
			log.Errorf("error shutting down SSH server: %s", err.Error())
			errs = append(errs, err)
		}
		cancel()
	}
	sshStrategy.serversMu.Lock()
	for address, server := range servers {
		if current, ok := sshStrategy.servers[address]; ok && current == server {
			delete(sshStrategy.servers, address)
			delete(sshStrategy.serverReady, address)
		}
	}
	if len(sshStrategy.servers) == 0 {
		sshStrategy.servers = nil
		sshStrategy.serverReady = nil
	}
	sshStrategy.serversMu.Unlock()
	if len(errs) > 0 {
		return fmt.Errorf("ssh stop errors: %w", errors.Join(errs...))
	}
	return nil
}

func (sshStrategy *SSHStrategy) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	sshStrategy.serversMu.RLock()
	server, ok := sshStrategy.servers[servConf.Address]
	ready := sshStrategy.serverReady[servConf.Address]
	sshStrategy.serversMu.RUnlock()
	if !ok {
		return nil
	}
	waitForSSHServer(ready)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Errorf("error shutting down SSH server on %s: %s", servConf.Address, err.Error())
		return err
	}
	sshStrategy.serversMu.Lock()
	delete(sshStrategy.servers, servConf.Address)
	delete(sshStrategy.serverReady, servConf.Address)
	sshStrategy.serversMu.Unlock()
	return nil
}

func waitForSSHServer(ready <-chan struct{}) {
	if ready != nil {
		<-ready
	}
}

func buildPrompt(user string, serverName string) string {
	return fmt.Sprintf("%s@%s:~$ ", user, serverName)
}
