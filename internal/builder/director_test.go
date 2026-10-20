package builder

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	defer server.Close()
	b := NewBuilder()
	d := NewDirector(b)

	coreConfig := &parser.BeelzebubCoreConfigurations{}
	coreConfig.Core.BeelzebubCloud.Enabled = true
	coreConfig.Core.BeelzebubCloud.URI = server.URL
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

func TestCloudEventSenderReusesConnectionsAndStopsOnClose(t *testing.T) {
	var connections atomic.Int64
	var requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}

	server.Start()
	defer server.Close()
	config := &parser.BeelzebubCoreConfigurations{}
	config.Core.BeelzebubCloud.Enabled = true
	config.Core.BeelzebubCloud.URI = server.URL
	config.Core.BeelzebubCloud.AuthToken = "synthetic-token"
	builder := NewBuilder()
	director := NewDirector(builder)
	built, err := director.BuildBeelzebub(config, nil)
	require.NoError(t, err)
	defer built.Close()
	for range 160 {
		director.beelzebubCloudStrategy(tracer.Event{Protocol: "HTTP", ID: "synthetic"})
	}

	require.Equal(t, int64(160), requests.Load())
	require.LessOrEqual(t, connections.Load(), int64(2), "legacy events must share a connection pool")
	require.NoError(t, built.Close())
	ok, err := built.cloudSender.SendEvent(tracer.Event{})
	require.Error(t, err)
	require.False(t, ok)
	require.Equal(t, int64(160), requests.Load(), "a stopped sender must reject further network requests")
}
