package builder

import (
	"net/http"
	"net/http/httptest"
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
}

func TestBeelzebubCloudStrategy_SendEventError(t *testing.T) {
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
				URI:       "http://localhost:9999",
				AuthToken: "test-token",
			},
		},
	}
	d := NewDirector(b)

	// Should not panic — just log error
	d.beelzebubCloudStrategy(tracer.Event{
		Protocol: "SSH",
		Status:   "Stateless",
		ID:       "test-id",
		User:     "root",
		Password: "secret",
	})
}

func TestBeelzebubCloudStrategy_SendEventSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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
	d := NewDirector(b)

	d.beelzebubCloudStrategy(tracer.Event{
		Protocol: "SSH",
		Status:   "Stateless",
		ID:       "test-id",
		User:     "root",
		Password: "secret",
	})
}

func TestBuildBeelzebub_InvalidLogPath(t *testing.T) {
	b := NewBuilder()
	d := NewDirector(b)

	coreConfig := &parser.BeelzebubCoreConfigurations{}
	coreConfig.Core.Logging.LogsPath = "/nonexistent/path/that/does/not/exist.log"

	_, err := d.BuildBeelzebub(coreConfig, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
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
