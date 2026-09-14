package builder

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/HTTP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/SSH"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TELNET"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderClose_LogFile(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	logFilePath := tmpDir + "/test.log"

	// Create a builder instance
	builder := NewBuilder()

	// Build logger which opens a log file
	loggingConfig := parser.Logging{
		Debug:               false,
		DebugReportCaller:   false,
		LogDisableTimestamp: true,
		LogsPath:            logFilePath,
	}

	err := builder.buildLogger(loggingConfig)
	assert.NoError(t, err)
	assert.NotNil(t, builder.logsFile)

	// Verify the log file exists and is open
	fileInfo, err := os.Stat(logFilePath)
	assert.NoError(t, err)
	assert.NotNil(t, fileInfo)

	// Close the builder
	err = builder.Close()
	assert.NoError(t, err)

	// Verify the log file is closed by attempting to write to it
	// Writing to a closed file should return an error
	_, err = builder.logsFile.WriteString("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file already closed")
}

func TestBuilderClose_NoLogFile(t *testing.T) {
	// Create a builder without opening a log file
	builder := NewBuilder()

	// Close should succeed even without a log file
	err := builder.Close()
	assert.NoError(t, err)
}

func TestBuilderClose_NilLogFile(t *testing.T) {
	// Create a builder with explicitly nil log file
	builder := &Builder{
		logsFile: nil,
	}

	// Close should succeed with nil log file
	err := builder.Close()
	assert.NoError(t, err)
}

func TestBuilderClose_RabbitMQCleanupCanRetry(t *testing.T) {
	origCloseChannel := amqpCloseChannel
	origCloseConnection := amqpCloseConnection
	t.Cleanup(func() {
		amqpCloseChannel = origCloseChannel
		amqpCloseConnection = origCloseConnection
	})

	channelCalls := 0
	connectionCalls := 0
	amqpCloseChannel = func(*amqp.Channel) error {
		channelCalls++
		if channelCalls == 1 {
			return fmt.Errorf("channel cleanup failure")
		}
		return nil
	}
	amqpCloseConnection = func(*amqp.Connection) error {
		connectionCalls++
		return fmt.Errorf("connection cleanup failure")
	}

	b := &Builder{
		rabbitMQChannel:    &amqp.Channel{},
		rabbitMQConnection: &amqp.Connection{},
	}

	err := b.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel cleanup failure")
	assert.Contains(t, err.Error(), "connection cleanup failure")
	assert.Equal(t, 1, channelCalls)
	assert.Equal(t, 1, connectionCalls)
	assert.NotNil(t, b.rabbitMQChannel)
	assert.NotNil(t, b.rabbitMQConnection)

	amqpCloseChannel = func(*amqp.Channel) error {
		channelCalls++
		return nil
	}
	amqpCloseConnection = func(*amqp.Connection) error {
		connectionCalls++
		return nil
	}

	require.NoError(t, b.Close())
	assert.Equal(t, 2, channelCalls)
	assert.Equal(t, 2, connectionCalls)
	assert.Nil(t, b.rabbitMQChannel)
	assert.Nil(t, b.rabbitMQConnection)
}

func TestSetTraceStrategy(t *testing.T) {
	b := NewBuilder()
	strategy := func(event tracer.Event) {}
	b.setTraceStrategy(strategy)
	if b.traceStrategy == nil {
		t.Errorf("expected traceStrategy to be set")
	}
}

func TestBuildLogger_InvalidPath(t *testing.T) {
	b := NewBuilder()
	cfg := parser.Logging{
		LogsPath: filepath.Join("/", "invalid", "path", "that", "does", "not", "exist.log"),
	}

	err := b.buildLogger(cfg)
	if err == nil {
		t.Fatalf("expected error for invalid log path, got nil")
	}
}

func TestBuilderBuild(t *testing.T) {
	b1 := NewBuilder()
	b1.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b2 := b1.build()

	if b2 == nil {
		t.Fatalf("expected build to return a new builder")
	}
	if b2.beelzebubCoreConfigurations != b1.beelzebubCoreConfigurations {
		t.Errorf("expected configurations to be copied")
	}
}

func TestBuilderRun_Empty(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}

	// Set trace strategy to avoid nil pointer
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	if err != nil {
		t.Errorf("expected no error running empty builder, got %v", err)
	}

	// Give a little time for the prometheus goroutine (which will just exit immediately since prometheus config is empty)
	time.Sleep(10 * time.Millisecond)
}

func TestBuilderRun_AllProtocols(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}

	// Add one service configuration for each protocol to hit all switch branches
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
		{Protocol: "ssh", Address: "127.0.0.1:0"},
		{Protocol: "tcp", Address: "127.0.0.1:0"},
		{Protocol: "telnet", Address: "127.0.0.1:0"},
		{Protocol: "mcp", Address: "127.0.0.1:0"},
	}

	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	if err != nil {
		t.Errorf("expected no error running builder with protocols, got %v", err)
	}

	time.Sleep(100 * time.Millisecond) // Wait a bit to let go funcs run and cover lines inside
}

func TestBuilderRun_UnknownProtocol(t *testing.T) {
	// We cannot easily test unknown protocol because it calls log.Fatalf
	// which causes the test to exit.
}

func TestBuildRabbitMQ_InvalidURI(t *testing.T) {
	b := NewBuilder()
	err := b.buildRabbitMQ("invalid-uri")
	if err == nil {
		t.Errorf("expected error building RabbitMQ with invalid URI")
	}
}

func TestBuilderRun_Prometheus(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			Prometheus: parser.Prometheus{
				Path: "/metrics",
				Port: "127.0.0.1:0",
			},
		},
	}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	assert.NoError(t, err)
	assert.NotNil(t, b.prometheusServer)

	time.Sleep(50 * time.Millisecond)
}

func TestBuilderReload(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	newConfigs := []parser.BeelzebubServiceConfiguration{
		{Protocol: "tcp", Address: "127.0.0.1:0"},
	}

	err = b.Reload(newConfigs)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	err = b.Close()
	assert.NoError(t, err)
}

func TestBuilderReload_WhenClosing(t *testing.T) {
	b := NewBuilder()
	b.closing.Store(true)

	err := b.Reload([]parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	})
	assert.NoError(t, err)
}

func TestBuilderReload_WithoutRun_ReturnsError(t *testing.T) {
	b := NewBuilder()

	err := b.Reload([]parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reload called before Run()")
}

func TestBuilderReload_RollbackOnFailure(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Reload with invalid config that will fail Run().
	// TCP Init with a missing port causes net.Listen to error out.
	invalidConfigs := []parser.BeelzebubServiceConfiguration{
		{Protocol: "tcp", Address: "invalid-address-no-port"},
	}
	err = b.Reload(invalidConfigs)
	require.Error(t, err) // should report the reload failure
	require.Contains(t, err.Error(), "reload failed, rolled back")

	// After rollback, old config should be running.
	// Close returns nil even though `closing` is already true
	// (set during the Reload → Close → rollback flow).
	err = b.Close()
	require.NoError(t, err)
}

func TestBuilderClose_Basic(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	err = b.Close()
	require.NoError(t, err)
}

func TestBuilderClose_WithBeelzebubCloud(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}
	// Set up a cloud instance to cover the Stop() path in Close()
	b.beelzebubCloud = plugins.InitBeelzebubCloud("http://localhost:9999", "test-token", nil, 0, nil)

	err := b.Run()
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Close should stop the cloud instance
	err = b.Close()
	require.NoError(t, err)
	// Second call should be a no-op
	err = b.Close()
	require.NoError(t, err)
}

func TestBuilderClose_WithProtocols(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	err = b.Close()
	assert.NoError(t, err)

	// Second close should be a no-op (atomic guard)
	err = b.Close()
	assert.NoError(t, err)
}

func TestBuilderReload_EndToEnd(t *testing.T) {
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port1 := l1.Addr().(*net.TCPAddr).Port
	l1.Close()

	l2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port2 := l2.Addr().(*net.TCPAddr).Port
	l2.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: fmt.Sprintf("127.0.0.1:%d", port1)},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(200 * time.Millisecond)

	// Port1 should be open after Run
	conn1, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port1), 2*time.Second)
	require.NoError(t, err, "port1 should be open after Run")
	conn1.Close()

	// Port2 should be closed before Reload
	_, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port2), 500*time.Millisecond)
	require.Error(t, err, "port2 should be closed before Reload")

	// Reload to port2
	newConfigs := []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: fmt.Sprintf("127.0.0.1:%d", port2)},
	}
	require.NoError(t, b.Reload(newConfigs))
	time.Sleep(200 * time.Millisecond)

	// Port1 should be closed after Reload
	_, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port1), 500*time.Millisecond)
	require.Error(t, err, "port1 should be closed after Reload")

	// Port2 should be open after Reload
	conn2, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port2), 2*time.Second)
	require.NoError(t, err, "port2 should be open after Reload")
	conn2.Close()

	require.NoError(t, b.Close())
}

func TestBuilderReload_BindFailureRollsBack(t *testing.T) {
	oldListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	oldAddr := oldListener.Addr().String()
	oldListener.Close()

	occupiedListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupiedListener.Close()
	occupiedAddr := occupiedListener.Addr().String()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: oldAddr},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())

	err = b.Reload([]parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: occupiedAddr},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reload failed, rolled back")

	conn, err := net.DialTimeout("tcp", oldAddr, time.Second)
	require.NoError(t, err, "old service should be listening after rollback")
	conn.Close()

	require.NoError(t, b.Close())
}

func TestBuilderReload_EndToEnd_CloudCallback(t *testing.T) {
	l1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port1 := l1.Addr().(*net.TCPAddr).Port
	l1.Close()

	l2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port2 := l2.Addr().(*net.TCPAddr).Port
	l2.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: fmt.Sprintf("127.0.0.1:%d", port1)},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(200 * time.Millisecond)

	// Port1 should be open after Run
	conn1, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port1), 2*time.Second)
	require.NoError(t, err, "port1 should be open after Run")
	conn1.Close()

	newConfigs := []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: fmt.Sprintf("127.0.0.1:%d", port2)},
	}
	require.NoError(t, b.handleCloudConfigChange(newConfigs, ""))
	time.Sleep(300 * time.Millisecond)

	// Port1 should be closed after reload
	_, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port1), 500*time.Millisecond)
	require.Error(t, err, "port1 should be closed after channel reload")

	// Port2 should be open after reload
	conn2, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port2), 2*time.Second)
	require.NoError(t, err, "port2 should be open after channel reload")
	conn2.Close()

	require.NoError(t, b.Close())
}

func TestBuilderRun_CloudEmptyConfig(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]"))
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
				Enabled:   true,
				URI:       ts.URL,
				AuthToken: "test-token",
			},
		},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.NoError(t, err)

	b.Close()
}

func TestBuilderClose_ServiceGroupStopAllError(t *testing.T) {
	// Start an HTTP server that will fail on Shutdown
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: addr},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	// Close should not fail even if StopAll logs errors
	err = b.Close()
	assert.NoError(t, err)
}

func TestBuilderClose_WithPrometheus(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			Prometheus: parser.Prometheus{
				Path: "/metrics",
				Port: "127.0.0.1:0",
			},
		},
	}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.NoError(t, err)
	require.NotNil(t, b.prometheusServer)

	err = b.Close()
	require.NoError(t, err)
	require.Nil(t, b.prometheusServer)
}

func TestBuilderDeployService_Success(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
		Address:  "127.0.0.1:0",
	}
	err := b.DeployService(cfg)
	require.NoError(t, err)
	assert.Len(t, b.beelzebubServicesConfiguration, 1)

	b.Close()
}

func TestBuilderDeployService_Duplicate(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:8080"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
		Address:  "127.0.0.1:8080",
	}
	err := b.DeployService(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	b.Close()
}

func TestBuilderDeployService_UnknownProtocol(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "nonexistent",
		Address:  "127.0.0.1:9999",
	}
	err := b.DeployService(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")

	b.Close()
}

func TestBuilderDeployService_Closing(t *testing.T) {
	b := NewBuilder()
	b.closing.Store(true)

	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "http",
		Address:  "127.0.0.1:8080",
	}
	err := b.DeployService(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "builder is closing")
}

func TestBuilderDeployService_InitFailure(t *testing.T) {
	// Occupy a port so the deploy's Init fails
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	defer l.Close()

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	// Use TCP protocol because Init is synchronous (net.Listen inside)
	// which returns the bind error immediately (unlike HTTP which is async)
	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "tcp",
		Address:  addr,
	}
	err = b.DeployService(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to deploy")

	b.Close()
}

type testSvcPlugin struct {
	name    string
	startFn func(context.Context) error
	stopped bool
}

func (p *testSvcPlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: p.name}
}

func (p *testSvcPlugin) Start(ctx context.Context) error {
	if p.startFn != nil {
		return p.startFn(ctx)
	}
	return nil
}

func (p *testSvcPlugin) Stop() {
	p.stopped = true
}

func TestBuilderRun_PluginServiceError(t *testing.T) {
	svc := &testSvcPlugin{
		name:    "test-svc-err-" + t.Name(),
		startFn: func(ctx context.Context) error { return fmt.Errorf("service start failure") },
	}
	plugin.Register(svc)

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	assert.NoError(t, err)

	b.Close()
}

func TestBuilderRun_PluginServiceSuccess_AndClose(t *testing.T) {
	svc := &testSvcPlugin{name: "test-svc-ok-" + t.Name()}
	plugin.Register(svc)

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	assert.Len(t, b.startedServices, 1)

	b.Close()

	assert.True(t, svc.stopped, "service plugin should be stopped on Close")
}

func TestBuilderRun_Prometheus_ListenError(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{
		Core: struct {
			Logging        parser.Logging        `yaml:"logging"`
			Tracings       parser.Tracings       `yaml:"tracings"`
			Prometheus     parser.Prometheus     `yaml:"prometheus"`
			BeelzebubCloud parser.BeelzebubCloud `yaml:"beelzebub-cloud"`
		}{
			Prometheus: parser.Prometheus{
				Path: "/metrics",
				Port: "127.0.0.1:1",
			},
		},
	}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	// Should not panic — log.Errorf is used instead of log.Fatalf
	err := b.Run()
	assert.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	b.Close()
}

func TestBuilderRun_CloudHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Response code: 500")
}

func TestBuilderDeployService_PreservesExistingConfigs(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "http", Address: "127.0.0.1:8080"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	require.NoError(t, b.Run())
	time.Sleep(50 * time.Millisecond)

	cfg := parser.BeelzebubServiceConfiguration{
		Protocol: "tcp",
		Address:  "127.0.0.1:9090",
	}
	err := b.DeployService(cfg)
	require.NoError(t, err)
	assert.Len(t, b.beelzebubServicesConfiguration, 2)

	b.Close()
}
