package TCP

import (
	"errors"
	"io"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

// TestMQTTCompat_FragmentedConnect verifies that the broker does not dispatch
// CONNECT from its first fixed-header byte. TCP may split the Remaining Length
// and payload into later reads, so CONNACK is valid only after the full packet.
func TestMQTTCompat_FragmentedConnect(t *testing.T) {
	addr, _ := vncStartService(t, "../../../../configurations/services/tcp-1883-mqtt.yaml")

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// MQTT 3.1.1 CONNECT with Clean Session and an empty client ID.
	packet := []byte{
		0x10, 0x0c,
		0x00, 0x04, 'M', 'Q', 'T', 'T',
		0x04, 0x02, 0x00, 0x3c,
		0x00, 0x00,
	}
	if _, err := conn.Write(packet[:1]); err != nil {
		t.Fatalf("write CONNECT first byte: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set partial-read deadline: %v", err)
	}
	var early [1]byte
	n, err := conn.Read(early[:])
	if n != 0 || err == nil {
		t.Fatalf("server responded before complete MQTT packet: n=%d data=%x err=%v", n, early[:n], err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("early read err = %v, want timeout while packet is incomplete", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set response deadline: %v", err)
	}
	if _, err := conn.Write(packet[1:]); err != nil {
		t.Fatalf("write CONNECT remainder: %v", err)
	}
	connAck := make([]byte, 4)
	if _, err := io.ReadFull(conn, connAck); err != nil {
		t.Fatalf("read CONNACK: %v", err)
	}
	want := []byte{0x20, 0x02, 0x00, 0x00}
	for i := range want {
		if connAck[i] != want[i] {
			t.Fatalf("CONNACK = %x, want %x", connAck, want)
		}
	}
}

func TestTCPFramingTransitionPreservesPipelinedBytes(t *testing.T) {
	addr, _ := startCfg(t, parser.BeelzebubServiceConfiguration{
		Framing: &parser.Framing{Mode: "fixed", FixedSize: 2},
		Commands: []parser.Command{
			{
				Regex:       regexp.MustCompile(`^AA$`),
				Handler:     "1",
				NextFraming: &parser.Framing{Mode: "fixed", FixedSize: 1},
			},
			{
				Regex:      regexp.MustCompile(`^B$`),
				Handler:    "2",
				CloseAfter: true,
			},
		},
	})

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte("AAB")); err != nil {
		t.Fatalf("write pipelined frames: %v", err)
	}
	responses := make([]byte, 2)
	if _, err := io.ReadFull(conn, responses); err != nil {
		t.Fatalf("read responses: %v", err)
	}
	if string(responses) != "12" {
		t.Fatalf("responses = %q, want 12", responses)
	}
}

func TestTCPTruncatedFramedMessageIsNotDispatched(t *testing.T) {
	addr, tr := startCfg(t, parser.BeelzebubServiceConfiguration{
		Framing: &parser.Framing{Mode: "fixed", FixedSize: 4},
		Commands: []parser.Command{{
			Regex:   regexp.MustCompile(`(?s).*`),
			Handler: "must-not-run",
			Name:    "catch_all",
		}},
	})

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("AB")); err != nil {
		t.Fatalf("write truncated frame: %v", err)
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := tcpConn.CloseWrite(); err != nil {
			t.Fatalf("close write: %v", err)
		}
	} else {
		conn.Close()
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var response [1]byte
	if n, err := conn.Read(response[:]); n != 0 || err == nil {
		t.Fatalf("truncated frame produced response: n=%d data=%x err=%v", n, response[:n], err)
	}
	conn.Close()
	time.Sleep(50 * time.Millisecond)

	for _, event := range tr.snapshot() {
		if event.Status == tracer.Interaction.String() && event.Handler == "catch_all" {
			t.Fatalf("truncated frame reached catch-all handler: %+v", event)
		}
	}
}
