package protocols

import (
	"errors"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStrategy struct {
	initErr error
	stopErr error
	initCfg parser.BeelzebubServiceConfiguration
}

func (m *mockStrategy) Init(cfg parser.BeelzebubServiceConfiguration, _ tracer.Tracer) error {
	m.initCfg = cfg
	return m.initErr
}

func (m *mockStrategy) Stop(parser.BeelzebubServiceConfiguration) error {
	return m.stopErr
}

func (m *mockStrategy) StopAll() error {
	return m.stopErr
}

func TestServiceGroup_InitServices_success(t *testing.T) {
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, &mockStrategy{}),
		map[string]ServiceStrategy{"ssh": &mockStrategy{}},
	)

	err := sg.InitServices([]parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
	})
	assert.NoError(t, err)
}

func TestServiceReadyMessage(t *testing.T) {
	assert.Equal(t,
		"HTTP :80 ready (Wordpress 6.0, 2 commands)",
		serviceReadyMessage(parser.BeelzebubServiceConfiguration{
			Protocol:    "http",
			Address:     ":80",
			Description: "Wordpress 6.0",
			Commands:    []parser.Command{{}, {}},
		}),
	)
	assert.Equal(t,
		"SSH :2222 ready (0 commands)",
		serviceReadyMessage(parser.BeelzebubServiceConfiguration{
			Protocol: "ssh",
			Address:  ":2222",
		}),
	)
}

func TestServiceGroup_InitServices_unknownProtocol(t *testing.T) {
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}),
		map[string]ServiceStrategy{},
	)

	err := sg.InitServices([]parser.BeelzebubServiceConfiguration{
		{Protocol: "unknown", Address: ":9999"},
	})
	assert.NoError(t, err)
}

func TestServiceGroup_InitServices_strategyError(t *testing.T) {
	strategy := &mockStrategy{initErr: errors.New("init failure")}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, strategy),
		map[string]ServiceStrategy{"ssh": strategy},
	)

	err := sg.InitServices([]parser.BeelzebubServiceConfiguration{{Protocol: "ssh", Address: ":22"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "error during init ssh")
}

func TestServiceGroup_Reload_noChanges(t *testing.T) {
	m := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, m),
		map[string]ServiceStrategy{"ssh": m},
	)

	configs := []parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
	}
	err := sg.InitServices(configs)
	require.NoError(t, err)

	err = sg.Reload(configs, configs)
	assert.NoError(t, err)
}

func TestServiceGroup_Reload_newService(t *testing.T) {
	ssh := &mockStrategy{}
	http := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, ssh, http),
		map[string]ServiceStrategy{"ssh": ssh, "http": http},
	)

	old := []parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
	}
	err := sg.InitServices(old)
	require.NoError(t, err)

	new := []parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
		{Protocol: "http", Address: ":8080"},
	}
	err = sg.Reload(old, new)
	assert.NoError(t, err)
}

func TestServiceGroup_Reload_removedService(t *testing.T) {
	ssh := &mockStrategy{}
	http := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, ssh, http),
		map[string]ServiceStrategy{"ssh": ssh, "http": http},
	)

	err := sg.InitServices([]parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
		{Protocol: "http", Address: ":8080"},
	})
	require.NoError(t, err)

	err = sg.Reload(
		[]parser.BeelzebubServiceConfiguration{
			{Protocol: "ssh", Address: ":2222"},
			{Protocol: "http", Address: ":8080"},
		},
		[]parser.BeelzebubServiceConfiguration{
			{Protocol: "ssh", Address: ":2222"},
		},
	)
	assert.NoError(t, err)
}

func TestServiceGroup_Reload_rollbackOnFailure(t *testing.T) {
	ssh := &mockStrategy{}
	bad := &mockStrategy{initErr: errors.New("start failure")}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, ssh, bad),
		map[string]ServiceStrategy{"ssh": ssh, "bad": bad},
	)

	err := sg.InitServices([]parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
	})
	require.NoError(t, err)

	err = sg.Reload(
		[]parser.BeelzebubServiceConfiguration{
			{Protocol: "ssh", Address: ":2222"},
		},
		[]parser.BeelzebubServiceConfiguration{
			{Protocol: "bad", Address: ":9999"},
		},
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reload failed, rolled back")
}

func TestServiceGroup_Reload_duplicateKey(t *testing.T) {
	configs := []parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
		{Protocol: "ssh", Address: ":2222"},
	}
	mapped := buildConfigMap(configs)
	assert.Len(t, mapped, 1)
}

func TestServiceGroup_StopAll(t *testing.T) {
	m := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, m),
		map[string]ServiceStrategy{"ssh": m},
	)

	err := sg.StopAll()
	assert.NoError(t, err)
}

func TestServiceGroup_configChanged_differentHash(t *testing.T) {
	a := parser.BeelzebubServiceConfiguration{Protocol: "ssh", Address: ":2222", Description: "old"}
	b := parser.BeelzebubServiceConfiguration{Protocol: "ssh", Address: ":2222", Description: "new"}
	assert.True(t, configChanged(a, b))
}

func TestServiceGroup_configChanged_sameHash(t *testing.T) {
	a := parser.BeelzebubServiceConfiguration{Protocol: "ssh", Address: ":2222"}
	b := parser.BeelzebubServiceConfiguration{Protocol: "ssh", Address: ":2222"}
	assert.False(t, configChanged(a, b))
}

func TestServiceGroup_Reload_RollbackWithStopError(t *testing.T) {
	ssh := &mockStrategy{}
	good := &mockStrategy{}
	bad := &mockStrategy{initErr: errors.New("start failure")}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, ssh, good, bad),
		map[string]ServiceStrategy{"ssh": ssh, "good": good, "bad": bad},
	)

	err := sg.InitServices([]parser.BeelzebubServiceConfiguration{
		{Protocol: "ssh", Address: ":2222"},
	})
	require.NoError(t, err)

	// Reload: remove ssh, add good (succeeds) and bad (fails Init).
	// good.stopErr triggers the rollback StopService error path.
	good.stopErr = errors.New("stop failure during rollback")
	err = sg.Reload(
		[]parser.BeelzebubServiceConfiguration{
			{Protocol: "ssh", Address: ":2222"},
		},
		[]parser.BeelzebubServiceConfiguration{
			{Protocol: "good", Address: ":8080"},
			{Protocol: "bad", Address: ":9999"},
		},
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reload failed, rolled back")
}

func TestStrategyForProtocol_CaseInsensitive(t *testing.T) {
	m := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, m),
		map[string]ServiceStrategy{"ssh": m},
	)

	strategy := sg.strategyForProtocol("SSH")
	assert.NotNil(t, strategy)

	strategy = sg.strategyForProtocol("Ssh")
	assert.NotNil(t, strategy)

	strategy = sg.strategyForProtocol("ssh")
	assert.NotNil(t, strategy)
}

func TestStrategyForProtocol_ReturnsNilForUnknown(t *testing.T) {
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}),
		map[string]ServiceStrategy{"ssh": &mockStrategy{}},
	)

	strategy := sg.strategyForProtocol("nonexistent")
	assert.Nil(t, strategy)
}

func TestServiceGroup_StrategyForProtocol_Locks(t *testing.T) {
	m := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, m),
		map[string]ServiceStrategy{"ssh": m},
	)

	strategy := sg.StrategyForProtocol("ssh")
	assert.NotNil(t, strategy)
	// Thread-safe public accessor should also handle nil map
	sg2 := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}),
		nil,
	)
	assert.Nil(t, sg2.StrategyForProtocol("ssh"))
}

func TestServiceGroup_InitService(t *testing.T) {
	m := &mockStrategy{}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, m),
		map[string]ServiceStrategy{"ssh": m},
	)

	err := sg.InitService(
		parser.BeelzebubServiceConfiguration{Protocol: "ssh", Address: ":2222"},
		m,
	)
	assert.NoError(t, err)
}

func TestServiceGroup_InitService_Error(t *testing.T) {
	m := &mockStrategy{initErr: errors.New("init failure")}
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}, m),
		map[string]ServiceStrategy{"ssh": m},
	)

	err := sg.InitService(
		parser.BeelzebubServiceConfiguration{Protocol: "ssh", Address: ":2222"},
		m,
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "init failure")
}

func TestStrategyForProtocol_NilMap(t *testing.T) {
	sg := NewServiceGroupWithStrategies(
		InitProtocolManager(func(tracer.Event) {}),
		nil,
	)

	strategy := sg.strategyForProtocol("ssh")
	assert.Nil(t, strategy)
}
