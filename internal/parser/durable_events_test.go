package parser

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEventSpoolConfiguration(t *testing.T) {
	var config BeelzebubCoreConfigurations
	require.NoError(t, yaml.Unmarshal([]byte("core:\n  beelzebub-cloud:\n    event-spool-dir: /persistent/events\n    event-spool-max-bytes: 2048\n    event-spool-max-records: 8\n"), &config))
	require.Equal(t, "/persistent/events", config.Core.BeelzebubCloud.EventSpoolDir)
	require.Equal(t, int64(2048), config.Core.BeelzebubCloud.EventSpoolMaxBytes)
	t.Setenv("BEELZEBUB_EVENT_SPOOL_DIR", "/override/events")
	t.Setenv("BEELZEBUB_EVENT_SPOOL_MAX_BYTES", "4096")
	t.Setenv("BEELZEBUB_EVENT_SPOOL_MAX_RECORDS", "16")
	require.NoError(t, applyEnvOverrides(&config))
	require.Equal(t, "/override/events", config.Core.BeelzebubCloud.EventSpoolDir)
	require.Equal(t, int64(4096), config.Core.BeelzebubCloud.EventSpoolMaxBytes)
	require.Equal(t, int64(16), config.Core.BeelzebubCloud.EventSpoolMaxRecords)
}

func TestEventSpoolInvalidEnvironmentLimits(t *testing.T) {
	for _, name := range []string{"BEELZEBUB_EVENT_SPOOL_MAX_BYTES", "BEELZEBUB_EVENT_SPOOL_MAX_RECORDS"} {
		for _, value := range []string{"0", "-1", "invalid"} {
			t.Run(name+value, func(t *testing.T) {
				t.Setenv(name, value)
				require.ErrorContains(t, applyEnvOverrides(&BeelzebubCoreConfigurations{}), name)
			})
		}
	}
}
