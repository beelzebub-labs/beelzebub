package TCP

import (
	"encoding/base64"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
)

type mockTracer struct {
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.events = append(m.events, event)
}

func newStrategyWithSessions() *TCPStrategy {
	return &TCPStrategy{Sessions: historystore.NewHistoryStore()}
}

func TestHandleTCPConnection_NoCommands_Legacy(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands:               []parser.Command{},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("hello world"))
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}

	assert.GreaterOrEqual(t, len(mt.events), 1)
	assert.Equal(t, tracer.Stateless.String(), mt.events[0].Status)
}

func TestHandleTCPConnection_WithBanner(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 2,
		Banner:                 "Welcome to SSH",
		Commands:               []parser.Command{},
	}
	strategy := newStrategyWithSessions()

	go handleTCPConnection(server, servConf, mt, strategy)

	buf := make([]byte, 64)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err == nil {
		assert.Contains(t, string(buf[:n]), "Welcome to SSH")
	}
	client.Close()
}

func TestHandleTCPConnection_WithMatchingCommand(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{
				Name:    "ls",
				Regex:   regexp.MustCompile(`^ls$`),
				Handler: "file1.txt\nfile2.txt",
			},
		},
	}
	strategy := newStrategyWithSessions()

	go handleTCPConnection(server, servConf, mt, strategy)

	client.Write([]byte("ls\n"))

	buf := make([]byte, 256)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err == nil {
		assert.Contains(t, string(buf[:n]), "file1.txt")
	}

	client.Close()
}

func TestHandleTCPConnection_UnmatchedCommand(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{
				Regex:   regexp.MustCompile(`^ls$`),
				Handler: "file1.txt",
			},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("unknown_cmd\n"))
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	foundNotFound := false
	for _, e := range mt.events {
		if e.Handler == "not_found" {
			foundNotFound = true
			break
		}
	}
	assert.True(t, foundNotFound, "expected a not_found handler event")
}

func TestTCPStrategy_Init(t *testing.T) {
	strategy := &TCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address:                "127.0.0.1:0",
		Description:            "test",
		DeadlineTimeoutSeconds: 2,
	}

	err := strategy.Init(servConf, mt)
	assert.NoError(t, err)
	assert.NotNil(t, strategy.Sessions)
}

func TestTCPStrategy_Init_InvalidAddress(t *testing.T) {
	strategy := &TCPStrategy{}
	mt := &mockTracer{}

	servConf := parser.BeelzebubServiceConfiguration{
		Address: "invalid-address",
	}

	err := strategy.Init(servConf, mt)
	assert.Error(t, err)
}

func TestHandleTCPConnection_CommandWithEmptyHandler(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{
				Regex:   regexp.MustCompile(`.*`),
				Handler: "",
			},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("anything\n"))
	time.Sleep(100 * time.Millisecond)
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	found := false
	for _, e := range mt.events {
		if strings.Contains(e.Msg, "Interaction") || e.Status == tracer.Interaction.String() {
			found = true
			break
		}
	}
	assert.True(t, found, "expected an interaction event")
}

func TestHandleTCPConnection_SessionStart(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{
				Regex:   regexp.MustCompile(`^ping$`),
				Handler: "pong",
			},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	// Close immediately to trigger session end event
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	// Should have at minimum session start and end events
	hasStart := false
	hasEnd := false
	for _, e := range mt.events {
		if e.Status == tracer.Start.String() {
			hasStart = true
		}
		if e.Status == tracer.End.String() {
			hasEnd = true
		}
	}
	assert.True(t, hasStart, "expected session start event")
	assert.True(t, hasEnd, "expected session end event")
}

func TestHandleTCPConnection_CommandWithNoHandlerAndUnprintableBytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands:               []parser.Command{},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	// This is a load of unprintable bytes to test the base64 encoding of the command in the event
	inputData := []byte{94, 92, 117, 102, 102, 102, 100, 92, 92, 206, 141, 92, 117, 102, 102, 102, 100, 92, 117, 102,
		102, 102, 100, 92, 117, 102, 102, 102, 100, 92, 117, 102, 102, 102, 100, 92, 117, 48, 48, 49, 53, 92, 117, 102,
	}

	client.Write(inputData)
	time.Sleep(100 * time.Millisecond)
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	found := false
	for _, e := range mt.events {
		if e.Command == base64.StdEncoding.EncodeToString(inputData) {
			found = true
			break
		}
	}

	assert.True(t, found, "expected an interaction event with command")
}
