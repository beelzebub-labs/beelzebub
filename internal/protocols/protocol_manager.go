package protocols

import (
	"errors"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

type ServiceStrategy interface {
	Init(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, tracer tracer.Tracer) error
	Stop(servConf parser.BeelzebubServiceConfiguration) error
	StopAll() error
}

type ProtocolManager struct {
	strategies []ServiceStrategy
	tracer     tracer.Tracer
}

func InitProtocolManager(tracerStrategy tracer.Strategy, strategies ...ServiceStrategy) *ProtocolManager {
	return &ProtocolManager{
		tracer:     tracer.GetInstance(tracerStrategy),
		strategies: strategies,
	}
}

// Deprecated: InitProtocolManager now accepts all strategies at construction.
// SetProtocolStrategy is kept for test backward compatibility and has no
// effect on InitService (which takes the strategy as a parameter directly).
func (pm *ProtocolManager) SetProtocolStrategy(strategy ServiceStrategy) {
	pm.strategies = append(pm.strategies, strategy)
}

func (pm *ProtocolManager) InitService(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, strategy ServiceStrategy) error {
	return strategy.Init(beelzebubServiceConfiguration, pm.tracer)
}

func (pm *ProtocolManager) StopAll() error {
	var errs []error
	for _, s := range pm.strategies {
		if err := s.StopAll(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (pm *ProtocolManager) StopService(servConf parser.BeelzebubServiceConfiguration, strategy ServiceStrategy) error {
	return strategy.Stop(servConf)
}
