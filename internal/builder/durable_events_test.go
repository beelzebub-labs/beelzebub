package builder

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/require"
)

func TestDurableEventsOptInAndOwnership(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	conf := &b.beelzebubCoreConfigurations.Core.BeelzebubCloud
	conf.Enabled = true
	conf.URI = "http://127.0.0.1:1"
	conf.AuthToken = "synthetic-token"
	require.NoError(t, b.startDurableEvents())
	require.Nil(t, b.durableEvents)
	conf.EventSpoolDir = filepath.Join(t.TempDir(), "events")
	require.NoError(t, b.startDurableEvents())
	require.NotNil(t, b.durableEvents)
	require.NoError(t, b.durableEvents.Enqueue(context.Background(), tracer.Event{ID: "synthetic-session"}))
	require.NoError(t, b.Close())
	require.ErrorIs(t, b.durableEvents.Enqueue(context.Background(), tracer.Event{}), plugins.ErrEventSpoolClosed)
	reopened, err := plugins.NewDurableEvents(conf.URI, conf.AuthToken, conf.EventSpoolDir, 0, 0)
	require.NoError(t, err)
	reopened.Close()
}

func TestDurableStartupFailureDoesNotStartServices(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	conf := &b.beelzebubCoreConfigurations.Core.BeelzebubCloud
	conf.Enabled = true
	conf.EventSpoolDir = filepath.Join(t.TempDir(), "events")
	// Missing credentials must fail before any listener/background service starts.
	require.ErrorContains(t, b.Run(), "cloud auth token")
	require.Nil(t, b.durableEvents)
	require.Nil(t, b.servicesCancel)
	require.NoDirExists(t, conf.EventSpoolDir)
}

func TestDurableOptOutDoesNotTouchConfiguredStorage(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	conf := &b.beelzebubCoreConfigurations.Core.BeelzebubCloud
	path := filepath.Join(t.TempDir(), "unusable-spool")
	require.NoError(t, os.WriteFile(path, []byte("operator-owned file"), 0600))
	conf.EventSpoolDir = path
	conf.Enabled = false
	require.NoError(t, b.startDurableEvents())
	conf.Enabled, conf.EventSpoolDir = true, ""
	require.NoError(t, b.startDurableEvents())
	require.Nil(t, b.durableEvents)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "operator-owned file", string(body))
}

func TestDurableEventsDisabledWithCloudOff(t *testing.T) {
	b := NewBuilder()
	b.beelzebubCoreConfigurations = &parser.BeelzebubCoreConfigurations{}
	b.beelzebubCoreConfigurations.Core.BeelzebubCloud.EventSpoolDir = t.TempDir()
	require.NoError(t, b.startDurableEvents())
	require.Nil(t, b.durableEvents)
}
