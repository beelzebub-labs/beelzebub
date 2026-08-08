package builder

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
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
	logsFile := builder.logsFile

	// Verify the log file exists and is open
	fileInfo, err := os.Stat(logFilePath)
	assert.NoError(t, err)
	assert.NotNil(t, fileInfo)

	// Close the builder
	err = builder.Close()
	assert.NoError(t, err)

	// Verify the log file is closed by attempting to write to it
	// Writing to a closed file should return an error
	_, err = logsFile.WriteString("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "file already closed")
	assert.NoError(t, builder.Close(), "Close should be idempotent")
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

	if b2 != b1 {
		t.Fatal("expected build to retain the initialized builder and its resources")
	}
}

type testStopper struct{ calls int }

func (s *testStopper) Stop() { s.calls++ }

func TestBuilderClose_StopsCloudWatcherOnce(t *testing.T) {
	watcher := &testStopper{}
	b := NewBuilder()
	b.cloudWatcher = watcher

	assert.NoError(t, b.Close())
	assert.NoError(t, b.Close())
	assert.Equal(t, 1, watcher.calls)
}

type reentrantStopService struct {
	b    *Builder
	done chan struct{}
}

func (s *reentrantStopService) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: "reentrant-stop", Version: "test"}
}
func (s *reentrantStopService) Start(context.Context) error { return nil }
func (s *reentrantStopService) Stop() {
	_ = s.b.publishRabbitMQ(amqp.Publishing{})
	close(s.done)
}

func TestBuilderClose_DoesNotHoldRabbitLockWhileStoppingServices(t *testing.T) {
	b := NewBuilder()
	probe := &reentrantStopService{b: b, done: make(chan struct{})}
	b.startedServices = []plugin.ServicePlugin{probe}

	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	select {
	case err := <-closed:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Builder.Close deadlocked while a service accessed RabbitMQ during Stop")
	}
	select {
	case <-probe.done:
	default:
		t.Fatal("service Stop was not called")
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
	assert.Nil(t, b.tcpStrategy, "empty runtime should not record an unstarted TCP strategy")
	t.Cleanup(func() {
		assert.NoError(t, b.Close())
	})

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
	assert.NotNil(t, b.tcpStrategy, "successful TCP init should be tracked for shutdown")
	t.Cleanup(func() {
		assert.NoError(t, b.Close())
	})

	time.Sleep(100 * time.Millisecond) // Wait a bit to let go funcs run and cover lines inside
}

func TestBuilderRun_UnknownProtocol(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "unknown", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	err := b.Run()
	if err == nil {
		t.Fatal("expected error for unknown protocol")
	}
}

func TestBuilderRun_RollsBackStartedServicesOnError(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test address: %v", err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release test address: %v", err)
	}

	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubServicesConfiguration = []parser.BeelzebubServiceConfiguration{
		{Protocol: "tcp", Address: address},
		{Protocol: "unknown", Address: "127.0.0.1:0"},
	}
	b.traceStrategy = func(event tracer.Event) {}

	if err := b.Run(); err == nil {
		t.Fatal("expected error for unknown protocol")
	}
	assert.Nil(t, b.tcpStrategy, "startup rollback should clear the TCP strategy")

	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("TCP listener was not released after startup failure: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close replacement listener: %v", err)
	}
}

func TestBuildRabbitMQ_InvalidURI(t *testing.T) {
	b := NewBuilder()
	err := b.buildRabbitMQ("invalid-uri")
	if err == nil {
		t.Errorf("expected error building RabbitMQ with invalid URI")
	}
}
