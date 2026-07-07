package TCP

import (
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/historystore"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
)

type mockTracer struct {
	mu     sync.Mutex
	events []tracer.Event
}

func (m *mockTracer) TraceEvent(event tracer.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

// snapshot returns a copy of the recorded events, safe to read while the
// connection handler goroutine is still running.
func (m *mockTracer) snapshot() []tracer.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tracer.Event, len(m.events))
	copy(out, m.events)
	return out
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

	events := mt.snapshot()
	assert.GreaterOrEqual(t, len(events), 1)
	assert.Equal(t, tracer.Stateless.String(), events[0].Status)
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
	for _, e := range mt.snapshot() {
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
	for _, e := range mt.snapshot() {
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
	for _, e := range mt.snapshot() {
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

// TestHandleTCPConnection_CommandRaw verifies that binary (non-UTF-8) client
// input is preserved byte-exact in Event.CommandRaw on the matched-command
// interactive path (complementing tcp_rawbytes_test.go, which covers the
// not_found path), while UTF-8 input leaves CommandRaw empty.
func TestHandleTCPConnection_CommandRaw(t *testing.T) {
	run := func(t *testing.T, input []byte) []tracer.Event {
		client, server := net.Pipe()
		defer client.Close()

		mt := &mockTracer{}
		servConf := parser.BeelzebubServiceConfiguration{
			Description:            "test",
			DeadlineTimeoutSeconds: 5,
			Commands: []parser.Command{
				{Regex: regexp.MustCompile(`(?s).*`), Handler: "ok"},
			},
		}
		strategy := newStrategyWithSessions()

		done := make(chan struct{})
		go func() {
			defer close(done)
			handleTCPConnection(server, servConf, mt, strategy)
		}()

		// Drain the server's response so its conn.Write does not block on the
		// synchronous net.Pipe, allowing the interaction event to be emitted.
		go io.Copy(io.Discard, client)

		client.Write(input)
		time.Sleep(100 * time.Millisecond)
		client.Close()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout")
		}
		return mt.snapshot()
	}

	t.Run("binary input populates CommandRaw byte-exact", func(t *testing.T) {
		// Arbitrary non-UTF-8 bytes (0xfe is never valid UTF-8).
		events := run(t, []byte{0xfe, 0x01, 0x80, 0xff, 0x00, 0x41})
		var found bool
		for _, e := range events {
			if e.Status == tracer.Interaction.String() {
				assert.Equal(t, "\\xfe\\x01\\x80\\xff\\x00A", e.CommandRaw)
				found = true
			}
		}
		assert.True(t, found, "expected an interaction event with CommandRaw")
	})

	t.Run("utf8 input leaves CommandRaw empty", func(t *testing.T) {
		events := run(t, []byte("hello\n"))
		for _, e := range events {
			if e.Status == tracer.Interaction.String() {
				assert.Empty(t, e.CommandRaw, "CommandRaw must be empty for UTF-8 input")
			}
		}
	})
}
