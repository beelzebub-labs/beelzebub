// Package protocols is responsible for managing the different protocols
package protocols

import (
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

// ServiceStrategy is the common interface that each protocol honeypot implements
type ServiceStrategy interface {
	Init(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration, tracer tracer.Tracer) error
}

type ProtocolManager struct {
	strategy ServiceStrategy
	tracer   tracer.Tracer
}

// InitProtocolManager is the method that initializes the protocol manager, receving the concrete tracer and the concrete service
func InitProtocolManager(tracerStrategy tracer.Strategy, serviceStrategy ServiceStrategy) *ProtocolManager {
	return &ProtocolManager{
		tracer:   tracer.GetInstance(tracerStrategy),
		strategy: serviceStrategy,
	}
}

func (pm *ProtocolManager) SetProtocolStrategy(strategy ServiceStrategy) {
	pm.strategy = strategy
}

// SetTracer selects a capture boundary before any services are initialized.
func (pm *ProtocolManager) SetTracer(capture tracer.Tracer) {
	pm.tracer = capture
}

// InitService is the method that initializes the honeypot
func (pm *ProtocolManager) InitService(beelzebubServiceConfiguration parser.BeelzebubServiceConfiguration) error {
	return pm.strategy.Init(beelzebubServiceConfiguration, pm.tracer)
}
