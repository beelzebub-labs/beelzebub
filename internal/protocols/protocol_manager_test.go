package protocols

import (
	"errors"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
	"testing"
)

type mockServiceStrategyValid struct {
}

func (mockServiceStrategy mockServiceStrategyValid) Init(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	return nil
}

func (mockServiceStrategy mockServiceStrategyValid) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	return nil
}

func (mockServiceStrategy mockServiceStrategyValid) StopAll() error {
	return nil
}

type mockServiceStrategyError struct {
}

func (mockServiceStrategy mockServiceStrategyError) Init(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	return errors.New("mockError")
}

func (mockServiceStrategy mockServiceStrategyError) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	return nil
}

func (mockServiceStrategy mockServiceStrategyError) StopAll() error {
	return nil
}

func TestInitServiceManager(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}

	protocolManager := InitProtocolManager(mockTraceStrategy, mockServiceStrategyValid{})

	assert.NotNil(t, protocolManager.tracer)
	assert.Len(t, protocolManager.strategies, 1)
}

func TestInitServiceSuccess(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}

	protocolManager := InitProtocolManager(mockTraceStrategy, mockServiceStrategyValid{})

	protocolManager.SetProtocolStrategy(mockServiceStrategyValid{})

	assert.Nil(t, protocolManager.InitService(parser.BeelzebubServiceConfiguration{}, mockServiceStrategyValid{}))
}

func TestInitServiceError(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}

	protocolManager := InitProtocolManager(mockTraceStrategy, mockServiceStrategyError{})

	assert.NotNil(t, protocolManager.InitService(parser.BeelzebubServiceConfiguration{}, mockServiceStrategyError{}))
}

func TestSetProtocolStrategy_ChangesStrategy(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}

	protocolManager := InitProtocolManager(mockTraceStrategy, mockServiceStrategyError{})

	// Error strategy initially returns an error.
	assert.Error(t, protocolManager.InitService(parser.BeelzebubServiceConfiguration{}, mockServiceStrategyError{}))

	// After switching to the valid strategy, InitService should succeed.
	protocolManager.SetProtocolStrategy(mockServiceStrategyValid{})
	assert.NoError(t, protocolManager.InitService(parser.BeelzebubServiceConfiguration{}, mockServiceStrategyValid{}))
}

func TestInitProtocolManager_TracerNonNil(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}

	protocolManager := InitProtocolManager(mockTraceStrategy, mockServiceStrategyValid{})

	assert.NotNil(t, protocolManager.tracer)
	assert.Len(t, protocolManager.strategies, 1)
}

func TestInitService_PassesConfigToStrategy(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}

	var captured parser.BeelzebubServiceConfiguration
	wantConf := parser.BeelzebubServiceConfiguration{Address: "0.0.0.0:8080", Protocol: "ssh"}

	rec := &recorderStrategy{captured: &captured}
	protocolManager := InitProtocolManager(mockTraceStrategy, rec)

	assert.NoError(t, protocolManager.InitService(wantConf, rec))
	assert.Equal(t, wantConf, captured)
}

type recorderStrategy struct {
	captured *parser.BeelzebubServiceConfiguration
}

func (r *recorderStrategy) Init(conf parser.BeelzebubServiceConfiguration, _ tracer.Tracer) error {
	*r.captured = conf
	return nil
}

func (r *recorderStrategy) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	return nil
}

func (r *recorderStrategy) StopAll() error {
	return nil
}

type mockServiceStrategyStopAllError struct {
}

func (mockServiceStrategy mockServiceStrategyStopAllError) Init(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	return nil
}

func (s mockServiceStrategyStopAllError) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	return nil
}

func (s mockServiceStrategyStopAllError) StopAll() error {
	return errors.New("stopAllError")
}

type mockServiceStrategyCallCounter struct {
	stopAllCalled int
}

func (m *mockServiceStrategyCallCounter) Init(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	return nil
}

func (m *mockServiceStrategyCallCounter) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	return nil
}

func (m *mockServiceStrategyCallCounter) StopAll() error {
	m.stopAllCalled++
	return nil
}

func TestStopAll_CallsAllStrategies(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}
	counter1 := &mockServiceStrategyCallCounter{}
	counter2 := &mockServiceStrategyCallCounter{}

	pm := InitProtocolManager(mockTraceStrategy, counter1, counter2)
	err := pm.StopAll()
	assert.NoError(t, err)
	assert.Equal(t, 1, counter1.stopAllCalled)
	assert.Equal(t, 1, counter2.stopAllCalled)
}

func TestStopAll_EmptyStrategies(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}
	pm := InitProtocolManager(mockTraceStrategy)

	err := pm.StopAll()
	assert.NoError(t, err)
}

func TestStopAll_ReturnsJoinedErrors(t *testing.T) {
	mockTraceStrategy := func(event tracer.Event) {}
	pm := InitProtocolManager(mockTraceStrategy, mockServiceStrategyStopAllError{}, mockServiceStrategyValid{})

	err := pm.StopAll()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stopAllError")
}
