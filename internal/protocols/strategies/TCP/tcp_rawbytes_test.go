package TCP

import (
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
)

func TestHexEscapeNonPrintable_PreservesPrintable(t *testing.T) {
	assert.Equal(t, "PING", hexEscapeNonPrintable([]byte("PING")))
}

func TestHexEscapeNonPrintable_EscapesCRLF(t *testing.T) {
	assert.Equal(t, "\\x0d\\x0a", hexEscapeNonPrintable([]byte("\r\n")))
}

func TestHexEscapeNonPrintable_EscapesNullByte(t *testing.T) {
	assert.Equal(t, "\\x00", hexEscapeNonPrintable([]byte{0x00}))
}

func TestHexEscapeNonPrintable_EscapesBackslash(t *testing.T) {
	assert.Equal(t, "\\x5c", hexEscapeNonPrintable([]byte{'\\'}))
}

func TestHexEscapeNonPrintable_PreservesHighByte(t *testing.T) {
	// A lone 0x80 is invalid UTF-8 and is exactly the kind of byte that
	// string() would replace with U+FFFD. It must survive as \x80.
	assert.Equal(t, "\\x80", hexEscapeNonPrintable([]byte{0x80}))
}

// A binary frame containing a non-UTF-8 byte (0x80, e.g. an LDAP BER tag)
// must be recoverable from CommandRaw rather than lost to U+FFFD.
func TestHandleTCPConnection_NonUTF8_PopulatesCommandRaw(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{Name: "noop", Regex: regexp.MustCompile(`^never_matches$`), Handler: ""},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	frame := []byte{0x30, 0x0c, 0x02, 0x01, 0x01, 0x80, 0x04, 0x00}
	client.Write(frame)
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}

	var got *tracer.Event
	for i := range mt.events {
		if mt.events[i].CommandRaw != "" {
			got = &mt.events[i]
			break
		}
	}

	assert.NotNil(t, got, "expected an event carrying CommandRaw")
	if got != nil {
		assert.Equal(t, hexEscapeNonPrintable(frame), got.CommandRaw)
		assert.Contains(t, got.CommandRaw, "\\x80", "the 0x80 byte must be recoverable")
	}
}

// Regression guard: plain ASCII input is valid UTF-8, so CommandRaw must stay
// empty — existing events are byte-identical to before this change.
func TestHandleTCPConnection_ASCII_LeavesCommandRawEmpty(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	mt := &mockTracer{}
	servConf := parser.BeelzebubServiceConfiguration{
		Description:            "test",
		DeadlineTimeoutSeconds: 5,
		Commands: []parser.Command{
			{Name: "noop", Regex: regexp.MustCompile(`^never_matches$`), Handler: ""},
		},
	}
	strategy := newStrategyWithSessions()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handleTCPConnection(server, servConf, mt, strategy)
	}()

	client.Write([]byte("anything\n"))
	client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for connection handler")
	}

	assert.NotEmpty(t, mt.events, "expected at least one traced event")
	for _, e := range mt.events {
		assert.Empty(t, e.CommandRaw, "ASCII traffic must not populate CommandRaw")
	}
}
