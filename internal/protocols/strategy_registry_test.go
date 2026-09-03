package protocols

import (
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/stretchr/testify/assert"
)

func TestRegisterStrategy_and_RegisteredStrategies(t *testing.T) {
	RegisterStrategy("testproto", func() ServiceStrategy { return &mockStrategy{} })
	names := RegisteredStrategies()
	assert.Contains(t, names, "testproto")
}

func TestRegisteredStrategies_returnsRegistered(t *testing.T) {
	names := RegisteredStrategies()
	assert.NotEmpty(t, names)
}

func TestNewServiceGroupFromRegistry(t *testing.T) {
	RegisterStrategy("regtest", func() ServiceStrategy { return &mockStrategy{} })

	sg := NewServiceGroupFromRegistry(func(event tracer.Event) {})
	assert.NotNil(t, sg)
	assert.NotNil(t, sg.strategies)
	assert.NotNil(t, sg.pm)

	strategy := sg.strategyForProtocol("regtest")
	assert.NotNil(t, strategy)
}

func TestServiceGroup_strategyForProtocol_unknown(t *testing.T) {
	sg := NewServiceGroupWithStrategies(nil, map[string]ServiceStrategy{})
	strategy := sg.strategyForProtocol("nonexistent")
	assert.Nil(t, strategy)
}

func TestNewServiceGroup_delegatesToRegistry(t *testing.T) {
	sg := NewServiceGroup(func(event tracer.Event) {})
	assert.NotNil(t, sg)
}

func TestRegisterStrategy_OverwriteWarning(t *testing.T) {
	RegisterStrategy("overwrite-test", func() ServiceStrategy { return &mockStrategy{} })
	RegisterStrategy("overwrite-test", func() ServiceStrategy { return &mockStrategy{} })

	names := RegisteredStrategies()
	assert.Contains(t, names, "overwrite-test")
}
