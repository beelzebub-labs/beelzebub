package protocols

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	log "github.com/sirupsen/logrus"
)

type ServiceGroup struct {
	pm         *ProtocolManager
	strategies map[string]ServiceStrategy
	mu         sync.Mutex
}

func NewServiceGroup(tracerStrategy tracer.Strategy) *ServiceGroup {
	return NewServiceGroupFromRegistry(tracerStrategy)
}

func NewServiceGroupWithStrategies(pm *ProtocolManager, strategies map[string]ServiceStrategy) *ServiceGroup {
	return &ServiceGroup{
		pm:         pm,
		strategies: strategies,
	}
}

func (sg *ServiceGroup) InitServices(configs []parser.BeelzebubServiceConfiguration) error {
	for _, cfg := range configs {
		strategy := sg.strategyForProtocol(cfg.Protocol)
		if strategy == nil {
			log.Warnf("protocol %q not managed, skipping", cfg.Protocol)
			continue
		}
		if err := sg.pm.InitService(cfg, strategy); err != nil {
			return fmt.Errorf("error during init %s: %w", cfg.Protocol, err)
		}
		log.Info(serviceReadyMessage(cfg))
	}
	return nil
}

func (sg *ServiceGroup) Reload(oldConfigs, newConfigs []parser.BeelzebubServiceConfiguration) error {
	sg.mu.Lock()
	defer sg.mu.Unlock()

	log.Infof("Hot-reload: updating %d services", len(newConfigs))

	oldMap := buildConfigMap(oldConfigs)
	newMap := buildConfigMap(newConfigs)

	var stoppedServices []parser.BeelzebubServiceConfiguration

	for key, oldCfg := range oldMap {
		newCfg, exists := newMap[key]
		if !exists || configChanged(oldCfg, newCfg) {
			if err := sg.pm.StopService(oldCfg, sg.strategyForProtocol(oldCfg.Protocol)); err != nil {
				log.Errorf("error stopping %s: %s", key, err.Error())
			}
			stoppedServices = append(stoppedServices, oldCfg)
		}
	}

	var startedServices []parser.BeelzebubServiceConfiguration
	var errs []error

	for _, newCfg := range newConfigs {
		key := newCfg.Protocol + ":" + newCfg.Address
		oldCfg, exists := oldMap[key]
		if exists && !configChanged(oldCfg, newCfg) {
			continue
		}
		strategy := sg.strategyForProtocol(newCfg.Protocol)
		if strategy == nil {
			log.Warnf("protocol %q not managed, skipping", newCfg.Protocol)
			continue
		}
		if err := sg.pm.InitService(newCfg, strategy); err != nil {
			errs = append(errs, fmt.Errorf("error starting %s: %w", key, err))
			continue
		}
		startedServices = append(startedServices, newCfg)
		log.Info(serviceReadyMessage(newCfg))
	}

	if len(errs) > 0 {
		for _, svc := range startedServices {
			key := svc.Protocol + ":" + svc.Address
			if err := sg.pm.StopService(svc, sg.strategyForProtocol(svc.Protocol)); err != nil {
				log.Errorf("error rolling back %s: %s", key, err.Error())
			}
		}
		for _, oldSvc := range stoppedServices {
			strategy := sg.strategyForProtocol(oldSvc.Protocol)
			if strategy == nil {
				continue
			}
			key := oldSvc.Protocol + ":" + oldSvc.Address
			if err := sg.pm.InitService(oldSvc, strategy); err != nil {
				log.Errorf("error restarting %s during rollback: %s", key, err.Error())
			} else {
				log.Infof("%s %s restarted (rollback)", strings.ToUpper(oldSvc.Protocol), oldSvc.Address)
			}
		}
		return fmt.Errorf("reload failed, rolled back: %w", errors.Join(errs...))
	}

	log.Infof("Started %d service(s)", len(newConfigs))
	return nil
}

func (sg *ServiceGroup) StrategyForProtocol(protocol string) ServiceStrategy {
	sg.mu.Lock()
	defer sg.mu.Unlock()
	if sg.strategies == nil {
		return nil
	}
	return sg.strategies[strings.ToLower(protocol)]
}

func (sg *ServiceGroup) InitService(cfg parser.BeelzebubServiceConfiguration, strategy ServiceStrategy) error {
	return sg.pm.InitService(cfg, strategy)
}

func (sg *ServiceGroup) StopAll() error {
	return sg.pm.StopAll()
}

func (sg *ServiceGroup) strategyForProtocol(protocol string) ServiceStrategy {
	if sg.strategies == nil {
		return nil
	}
	return sg.strategies[strings.ToLower(protocol)]
}

func buildConfigMap(configs []parser.BeelzebubServiceConfiguration) map[string]parser.BeelzebubServiceConfiguration {
	m := make(map[string]parser.BeelzebubServiceConfiguration, len(configs))
	for _, cfg := range configs {
		key := cfg.Protocol + ":" + cfg.Address
		if _, exists := m[key]; exists {
			log.Warnf("duplicate service %s in config, ignoring duplicate", key)
			continue
		}
		m[key] = cfg
	}
	return m
}

func configChanged(oldCfg, newCfg parser.BeelzebubServiceConfiguration) bool {
	oldHash, err1 := oldCfg.HashCode()
	newHash, err2 := newCfg.HashCode()
	if err1 != nil || err2 != nil {
		return true
	}
	return oldHash != newHash
}

func serviceReadyMessage(cfg parser.BeelzebubServiceConfiguration) string {
	message := fmt.Sprintf("%s %s ready", strings.ToUpper(cfg.Protocol), cfg.Address)
	if cfg.Description != "" {
		message += fmt.Sprintf(" (%s, %d commands)", cfg.Description, len(cfg.Commands))
	} else {
		message += fmt.Sprintf(" (%d commands)", len(cfg.Commands))
	}
	return message
}
