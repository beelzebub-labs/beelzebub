package builder

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/HTTP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/SSH"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TELNET"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupRabbitMQContainer(t *testing.T) (testcontainers.Container, string) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			message := strings.ToLower(fmt.Sprint(recovered))
			if strings.Contains(message, "docker") {
				t.Skipf("Docker unavailable, skipping RabbitMQ test: %v", recovered)
			}
			panic(recovered)
		}
	}()

	ctx := context.Background()
	container, err := rabbitmq.RunContainer(ctx,
		testcontainers.WithImage("rabbitmq:3.13-alpine"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Server startup complete").
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		if strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
			t.Skip("Docker not available, skipping RabbitMQ test")
		}
		t.Fatalf("failed to start RabbitMQ container: %s", err.Error())
	}

	uri, err := container.PortEndpoint(ctx, "5672/tcp", "amqp")
	require.NoError(t, err)

	t.Cleanup(func() {
		container.Terminate(ctx)
	})

	return container, uri
}

func TestBuildRabbitMQ_Success(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	b := NewBuilder()
	err := b.buildRabbitMQ(uri)
	assert.NoError(t, err)
	assert.NotNil(t, b.rabbitMQConnection)
	assert.NotNil(t, b.rabbitMQChannel)

	b.Close()
}

func TestBuildRabbitMQ_SuccessWithQueueDeclare(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	b := NewBuilder()
	err := b.buildRabbitMQ(uri)
	require.NoError(t, err)

	q, err := b.rabbitMQChannel.QueueInspect(RabbitmqQueueName)
	require.NoError(t, err)
	assert.Equal(t, RabbitmqQueueName, q.Name)

	b.Close()
}

func TestBuildRabbitMQ_ChannelError(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	origDial := amqpDial
	defer func() { amqpDial = origDial }()

	amqpDial = func(u string) (*amqp091.Connection, error) {
		conn, err := amqp091.Dial(u)
		if err != nil {
			return nil, err
		}
		conn.Close()
		return conn, nil
	}

	b := NewBuilder()
	err := b.buildRabbitMQ(uri)
	require.Error(t, err)
	assert.ErrorIs(t, err, amqp091.ErrClosed)
}

func TestBuildRabbitMQ_QueueDeclareError(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	conn, err := amqp091.Dial(uri)
	require.NoError(t, err)
	defer conn.Close()

	ch, err := conn.Channel()
	require.NoError(t, err)
	defer ch.Close()

	_, err = ch.QueueDeclare(RabbitmqQueueName, true, false, false, false, nil)
	require.NoError(t, err)

	b := NewBuilder()
	err = b.buildRabbitMQ(uri)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PRECONDITION_FAILED")
}

func TestBuilderClose_WithRabbitMQ_Success(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	tmpDir := t.TempDir()
	logFilePath := filepath.Join(tmpDir, "test.log")

	b := NewBuilder()

	err := b.buildLogger(parser.Logging{
		Debug:               false,
		DebugReportCaller:   false,
		LogDisableTimestamp: true,
		LogsPath:            logFilePath,
	})
	require.NoError(t, err)

	err = b.buildRabbitMQ(uri)
	require.NoError(t, err)

	err = b.Close()
	assert.NoError(t, err)
	assert.Nil(t, b.rabbitMQConnection)
	assert.Nil(t, b.rabbitMQChannel)
}

func TestBuilderClose_ChannelCloseError(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	origCloseCh := amqpCloseChannel
	defer func() { amqpCloseChannel = origCloseCh }()

	amqpCloseChannel = func(ch *amqp091.Channel) error {
		return errors.New("channel close simulated error")
	}

	b := NewBuilder()
	err := b.buildRabbitMQ(uri)
	require.NoError(t, err)
	require.NotNil(t, b.rabbitMQConnection)
	require.NotNil(t, b.rabbitMQChannel)

	err = b.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel close simulated error")

	assert.Nil(t, b.rabbitMQConnection)
	assert.NotNil(t, b.rabbitMQChannel)

	amqpCloseChannel = origCloseCh
	require.NoError(t, b.Close())
	assert.Nil(t, b.rabbitMQConnection)
	assert.Nil(t, b.rabbitMQChannel)
}

func TestBuilderClose_ConnectionCloseError(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	origCloseConn := amqpCloseConnection
	defer func() { amqpCloseConnection = origCloseConn }()

	amqpCloseConnection = func(conn *amqp091.Connection) error {
		return errors.New("connection close simulated error")
	}

	b := NewBuilder()
	err := b.buildRabbitMQ(uri)
	require.NoError(t, err)
	require.NotNil(t, b.rabbitMQConnection)
	require.NotNil(t, b.rabbitMQChannel)

	err = b.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection close simulated error")

	assert.NotNil(t, b.rabbitMQConnection)
	assert.Nil(t, b.rabbitMQChannel)

	amqpCloseConnection = origCloseConn
	require.NoError(t, b.Close())
	assert.Nil(t, b.rabbitMQConnection)
	assert.Nil(t, b.rabbitMQChannel)
}

func TestBuilderClose_LogFileCloseError(t *testing.T) {
	tmpDir := t.TempDir()
	logFilePath := filepath.Join(tmpDir, "test.log")

	b := NewBuilder()
	err := b.buildLogger(parser.Logging{
		Debug:               false,
		DebugReportCaller:   false,
		LogDisableTimestamp: true,
		LogsPath:            logFilePath,
	})
	require.NoError(t, err)
	require.NotNil(t, b.logsFile)

	b.logsFile.Close()

	err = b.Close()
	assert.NoError(t, err)
}

func TestBuilderClose_CloudAndRabbitMQ(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"1","config":"apiVersion: \"v1\"\nprotocol: \"http\"\naddress: \"127.0.0.1:0\"\ndescription: \"test\"\ncommands:\n  - regex: \".*\"\n    handler: \"test\"\n    statusCode: 200\n","tokenId":"token"}]`))
	}))
	defer ts.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			Tracings: parser.Tracings{
				RabbitMQ: parser.RabbitMQ{
					URI: uri,
				},
			},
			BeelzebubCloud: parser.BeelzebubCloud{
				Enabled:                true,
				URI:                    ts.URL,
				AuthToken:              "test-token",
				PollingIntervalSeconds: 9999,
			},
		},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	require.Len(t, b.beelzebubServicesConfiguration, 1)

	err := b.Close()
	assert.NoError(t, err)
	assert.Nil(t, b.rabbitMQConnection)
	assert.Nil(t, b.rabbitMQChannel)
}

func TestBuilderRun_CloudWithValidConfig(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"1","config":"apiVersion: \"v1\"\nprotocol: \"http\"\naddress: \"127.0.0.1:0\"\ndescription: \"test\"\ncommands:\n  - regex: \".*\"\n    handler: \"test\"\n    statusCode: 200\n","tokenId":"token"}]`))
	}))
	defer ts.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			BeelzebubCloud: parser.BeelzebubCloud{
				Enabled:                true,
				URI:                    ts.URL,
				AuthToken:              "test-token",
				PollingIntervalSeconds: 9999,
			},
		},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.NoError(t, err)
	require.Len(t, b.beelzebubServicesConfiguration, 1)
	require.Equal(t, "http", b.beelzebubServicesConfiguration[0].Protocol)

	b.Close()
}

func TestBuilderMCP_DeployCallback(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "mcp", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "mcp",
		Address:  "127.0.0.1:0",
	}
	err = b.DeployService(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	b.Close()
}

func TestBuilderBuild_WithRabbitMQ(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	b := NewBuilder()
	err := b.buildRabbitMQ(uri)
	require.NoError(t, err)
	require.NotNil(t, b.rabbitMQConnection)

	bcopy := b.build()
	assert.Nil(t, bcopy.rabbitMQChannel)
	assert.Nil(t, bcopy.rabbitMQConnection)
	assert.NotNil(t, b.rabbitMQConnection, "original builder retains connection")
}

func TestBuilderRun_InitServicesError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "tcp", Address: addr},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err = b.Run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error during init tcp")

	l.Close()
}

func TestBuilderRun_DebugLogging(t *testing.T) {
	b := NewBuilder()
	err := b.buildLogger(parser.Logging{
		Debug:               true,
		DebugReportCaller:   false,
		LogDisableTimestamp: true,
	})
	require.NoError(t, err)

	b.Close()
}

func TestBuilderCloud_CallbackInvoked(t *testing.T) {
	pollCount := int32(0)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pollCount, 1)
		w.WriteHeader(http.StatusOK)
		if atomic.LoadInt32(&pollCount) == 1 {
			w.Write([]byte(`[{"id":"1","config":"apiVersion: \"v1\"\nprotocol: \"http\"\naddress: \"127.0.0.1:0\"\ndescription: \"initial\"\ncommands:\n  - regex: \".*\"\n    handler: \"test\"\n    statusCode: 200\n","tokenId":"token"}]`))
		} else {
			w.Write([]byte(`[{"id":"1","config":"apiVersion: \"v1\"\nprotocol: \"http\"\naddress: \"127.0.0.1:0\"\ndescription: \"changed\"\ncommands:\n  - regex: \".*\"\n    handler: \"test\"\n    statusCode: 200\n","tokenId":"token"}]`))
		}
	}))
	defer ts.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			BeelzebubCloud: parser.BeelzebubCloud{
				Enabled:                true,
				URI:                    ts.URL,
				AuthToken:              "test-token",
				PollingIntervalSeconds: 1,
			},
		},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(2500 * time.Millisecond)

	b.Close()
}

type failListener struct {
	net.Listener
	closeErr error
}

func (f *failListener) Close() error {
	f.Listener.Close()
	return f.closeErr
}

type failOnStopAllStrategy struct {
	protocols.ServiceStrategy
}

func (f *failOnStopAllStrategy) StopAll() error {
	return errors.New("stop all simulated error")
}

type noopStrategy struct{}

func (n *noopStrategy) Init(_ parser.BeelzebubServiceConfiguration, _ tracer.Tracer) error {
	return nil
}
func (n *noopStrategy) Stop(_ parser.BeelzebubServiceConfiguration) error { return nil }
func (n *noopStrategy) StopAll() error                                    { return nil }

func TestBuilderClose_StopAllError(t *testing.T) {
	pm := protocols.InitProtocolManager(func(event tracer.Event) {}, &failOnStopAllStrategy{})
	sg := protocols.NewServiceGroupWithStrategies(pm, nil)

	b := NewBuilder()
	b.serviceGroup = sg

	err := b.Close()
	assert.NoError(t, err)
	assert.NotNil(t, b.serviceGroup, "Close does not nil the serviceGroup field")
}

func TestBuilderClose_PrometheusShutdownError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	failL := &failListener{Listener: l, closeErr: errors.New("prometheus shutdown error")}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	go srv.Serve(failL)
	time.Sleep(50 * time.Millisecond)

	b := NewBuilder()
	b.prometheusServer = srv

	err = b.Close()
	assert.NoError(t, err)
	assert.Nil(t, b.prometheusServer)
}

func TestBuilderReload_GoroutineError(t *testing.T) {
	_, uri := setupRabbitMQContainer(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"1","config":"apiVersion: \"v1\"\nprotocol: \"http\"\naddress: \"127.0.0.1:0\"\ndescription: \"test\"\ncommands:\n  - regex: \".*\"\n    handler: \"test\"\n    statusCode: 200\n","tokenId":"token"}]`))
	}))
	defer ts.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			Tracings: parser.Tracings{
				RabbitMQ: parser.RabbitMQ{
					URI: uri,
				},
			},
			BeelzebubCloud: parser.BeelzebubCloud{
				Enabled:                true,
				URI:                    ts.URL,
				AuthToken:              "test-token",
				PollingIntervalSeconds: 9999,
			},
		},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	occupiedAddr := l.Addr().String()

	err = b.handleCloudConfigChange([]parser.BeelzebubServiceConfiguration{
		{Protocol: "tcp", Address: occupiedAddr},
	}, "")
	require.Error(t, err)

	l.Close()
	b.Close()
}
