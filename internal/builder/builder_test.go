package builder

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
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
