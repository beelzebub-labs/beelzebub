package builder

import (
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDirector(t *testing.T) {
	b := NewBuilder()
	d := NewDirector(b)

	assert.Same(t, b, d.builder)
}

func TestBuildBeelzebub_Standard(t *testing.T) {
	b := NewBuilder()
	d := NewDirector(b)

	result, err := d.BuildBeelzebub(&parser.BeelzebubCoreConfigurations{}, nil)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotNil(t, result.traceStrategy)
}

func TestBuildBeelzebub_StoresServicesConfig(t *testing.T) {
	b := NewBuilder()
	d := NewDirector(b)

	services := []parser.BeelzebubServiceConfiguration{
		{Address: "0.0.0.0:2222", Protocol: "ssh"},
		{Address: "0.0.0.0:8080", Protocol: "http"},
	}

	result, err := d.BuildBeelzebub(&parser.BeelzebubCoreConfigurations{}, services)
	require.NoError(t, err)
	assert.Equal(t, services, result.beelzebubServicesConfiguration)
}

func TestBuildBeelzebub_BeelzebubCloud(t *testing.T) {
	b := NewBuilder()
	d := NewDirector(b)

	coreConfig := &parser.BeelzebubCoreConfigurations{}
	coreConfig.Core.BeelzebubCloud.Enabled = true
	coreConfig.Core.BeelzebubCloud.URI = "http://localhost:8080"
	coreConfig.Core.BeelzebubCloud.AuthToken = "token"

	result, err := d.BuildBeelzebub(coreConfig, nil)

	require.NoError(t, err)
	assert.NotNil(t, result.traceStrategy)

	// Verify the strategy is callable without panicking.
	d.beelzebubCloudStrategy(tracer.Event{})
}

func TestStandardOutStrategy(t *testing.T) {
	d := NewDirector(NewBuilder())

	// Verify the strategy handles a complete event without panicking.
	d.standardOutStrategy(tracer.Event{
		Protocol: "SSH",
		Status:   "Stateless",
		ID:       "test-id",
		User:     "root",
		Password: "secret",
	})
}

// Cannot easily test RabbitMQ connection in unit tests without a mock
