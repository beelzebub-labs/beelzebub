package protocols

import (
	"sort"
	"sync"

	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	log "github.com/sirupsen/logrus"
)

var (
	strategyFactories map[string]func() ServiceStrategy
	strategyFactoryMu sync.RWMutex
)

func RegisterStrategy(name string, factory func() ServiceStrategy) {
	strategyFactoryMu.Lock()
	defer strategyFactoryMu.Unlock()
	if strategyFactories == nil {
		strategyFactories = make(map[string]func() ServiceStrategy)
	}
	if _, exists := strategyFactories[name]; exists {
		log.Warnf("strategy %q already registered, overwriting", name)
	}
	strategyFactories[name] = factory
}

func NewServiceGroupFromRegistry(tracerStrategy tracer.Strategy) *ServiceGroup {
	strategyFactoryMu.RLock()
	defer strategyFactoryMu.RUnlock()

	keys := make([]string, 0, len(strategyFactories))
	for k := range strategyFactories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var services []ServiceStrategy
	strategyMap := make(map[string]ServiceStrategy, len(keys))
	for _, name := range keys {
		factory := strategyFactories[name]
		s := factory()
		services = append(services, s)
		strategyMap[name] = s
	}

	return NewServiceGroupWithStrategies(
		InitProtocolManager(tracerStrategy, services...),
		strategyMap,
	)
}

func RegisteredStrategies() []string {
	strategyFactoryMu.RLock()
	defer strategyFactoryMu.RUnlock()
	keys := make([]string, 0, len(strategyFactories))
	for k := range strategyFactories {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
