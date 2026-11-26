package builder

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type recordingEventChannel struct {
	body          amqp.Publishing
	exchange, key string
	calls         int
	fail          bool
}

func (c *recordingEventChannel) QueueDeclare(name string, _ bool, _ bool, _ bool, _ bool, _ amqp.Table) (amqp.Queue, error) {
	return amqp.Queue{Name: name}, nil
}
func (c *recordingEventChannel) Close() error { return nil }
func (c *recordingEventChannel) PublishWithContext(_ context.Context, exchange, key string, _ bool, _ bool, msg amqp.Publishing) error {
	c.calls++
	c.exchange = exchange
	c.key = key
	c.body = msg

	if c.fail {
		return errors.New("synthetic publish failure")
	}
	return nil
}

func TestRabbitTracePreservesRoutingAndJSON(t *testing.T) {
	for _, fail := range []bool{false, true} {
		channel := &recordingEventChannel{fail: fail}
		b := NewBuilder()
		b.rabbitMQChannel = channel
		event := tracer.Event{ID: "synthetic-received", Protocol: "HTTP", Body: "synthetic body"}
		NewDirector(b).rabbitMQTraceStrategy(event)
		require.Equal(t, 1, channel.calls)
		require.Empty(t, channel.exchange)
		require.Equal(t, RabbitmqQueueName, channel.key)
		require.Equal(t, "application/json", channel.body.ContentType)
		var received tracer.Event
		require.NoError(t, json.Unmarshal(channel.body.Body, &received))
		require.Equal(t, event, received)
	}
}

type failingCloudSender struct {
	stopped bool
	calls   int
}

func (s *failingCloudSender) SendEvent(tracer.Event) (bool, error) {
	s.calls++
	return false, errors.New("synthetic send failure")
}

func (s *failingCloudSender) Stop() { s.stopped = true }
func TestDirectorInitializationAndSenderFailures(t *testing.T) {
	b := NewBuilder()
	d := NewDirector(b)
	conf := &parser.BeelzebubCoreConfigurations{}
	conf.Core.Logging.LogsPath = filepath.Join(t.TempDir(), "missing", "log")
	_, err := d.BuildBeelzebub(conf, nil)
	require.Error(t, err)
	conf.Core.Logging.LogsPath = ""
	conf.Core.Tracings.RabbitMQ.Enabled = true
	conf.Core.Tracings.RabbitMQ.URI = "invalid-uri"
	_, err = d.BuildBeelzebub(conf, nil)
	require.Error(t, err)
	require.NotPanics(t, func() { d.beelzebubCloudStrategy(tracer.Event{}) })
	sender := &failingCloudSender{}
	b.cloudSender = sender
	d.beelzebubCloudStrategy(tracer.Event{})
	require.Equal(t, 1, sender.calls)
	require.NoError(t, b.Close())
	require.True(t, sender.stopped)
}

type lifecycleService struct {
	name          string
	err           error
	ctx           context.Context
	starts, stops atomic.Int64
}

func (s *lifecycleService) Metadata() plugin.Metadata { return plugin.Metadata{Name: s.name} }
func (s *lifecycleService) Start(ctx context.Context) error {
	s.ctx = ctx
	s.starts.Add(1)
	return s.err
}

func (s *lifecycleService) Stop() { s.stops.Add(1) }
func TestBuilderStartsStopsServicesAndPreservesFailureIsolation(t *testing.T) {
	good := &lifecycleService{name: "synthetic-good-" + uuid.NewString()}
	bad := &lifecycleService{name: "synthetic-bad-" + uuid.NewString(), err: errors.New("synthetic start failure")}
	plugin.Register(good)
	plugin.Register(bad)
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.traceStrategy = func(tracer.Event) {}
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.Run())
	require.Equal(t, int64(1), good.starts.Load())
	require.Equal(t, int64(1), bad.starts.Load())
	require.NoError(t, b.Close())
	require.ErrorIs(t, good.ctx.Err(), context.Canceled)
	require.Equal(t, int64(1), good.stops.Load())
	require.Zero(t, bad.stops.Load())
}

func TestBuilderFailedStartupClosesPollingAndSpool(t *testing.T) {
	for _, empty := range []bool{false, true} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listener.Close() })
		config := "apiVersion: v1\nprotocol: tcp\naddress: " + listener.Addr().String() + "\ndeadlineTimeoutSeconds: 1\n"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if empty {
				w.Write([]byte("[]"))
				return
			}
			json.NewEncoder(w).Encode([]plugins.HoneypotConfigResponseDTO{{ID: "synthetic", Config: config}})
		}))

		t.Cleanup(server.Close)
		core := &parser.BeelzebubCoreConfigurations{}
		core.Core.Prometheus = parser.Prometheus{Enabled: true, Path: "/metrics", Port: "127.0.0.1:0"}
		core.Core.BeelzebubCloud.Enabled = true
		core.Core.BeelzebubCloud.URI = server.URL
		core.Core.BeelzebubCloud.AuthToken = "synthetic-token"
		core.Core.BeelzebubCloud.EventSpoolDir = t.TempDir()
		b := NewBuilder()
		b.beelzebubCoreConfigurations = core
		b.traceStrategy = func(tracer.Event) {}
		t.Cleanup(func() { _ = b.Close() })
		runErr := b.Run()
		require.Error(t, runErr)
		require.NotNil(t, b.prometheusServer)
		metricsListener, err := net.Listen("tcp", b.prometheusServer.Addr)
		require.NoError(t, err, "failed startup must release the metrics listener as well as the spool")
		require.NoError(t, metricsListener.Close())

		if empty {
			require.ErrorContains(t, runErr, "no honeypots configuration")
		} else {
			require.ErrorContains(t, runErr, "error during init protocol")
		}

		reopened, err := plugins.NewDurableEvents(server.URL, "synthetic-token", core.Core.BeelzebubCloud.EventSpoolDir, 0, 0)
		require.NoError(t, err)
		reopened.Close()
		require.NotNil(t, b.cloudPollingStop)
		require.NoError(t, b.Close())
		listener.Close()
		server.Close()
	}
}

func TestBuilderDebugLoggingAndClosedFileError(t *testing.T) {
	previous := log.GetLevel()
	defer log.SetLevel(previous)
	b := NewBuilder()
	require.NoError(t, b.buildLogger(parser.Logging{Debug: true}))
	require.Equal(t, log.DebugLevel, log.GetLevel())
	file, err := os.CreateTemp(t.TempDir(), "synthetic-log")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	b.logsFile = file
	require.Error(t, b.Close())
}
