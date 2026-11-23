package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
)

const RabbitmqQueueName = "event"

var (
	amqpDial            = amqp.Dial
	amqpCloseChannel    = func(ch *amqp.Channel) error { return ch.Close() }
	amqpCloseConnection = func(conn *amqp.Connection) error { return conn.Close() }
)

type Builder struct {
	beelzebubServicesConfiguration []parser.BeelzebubServiceConfiguration
	beelzebubCoreConfigurations    *parser.BeelzebubCoreConfigurations
	traceStrategy                  tracer.Strategy
	rabbitMQChannel                *amqp.Channel
	rabbitMQConnection             *amqp.Connection
	logsFile                       *os.File
	startedServices                []plugin.ServicePlugin
	servicesCancel                 context.CancelFunc

	serviceGroup     *protocols.ServiceGroup
	prometheusServer *http.Server
	beelzebubCloud   *plugins.BeelzebubCloud

	reloadMu sync.Mutex
	closing  atomic.Bool
}

func (b *Builder) setTraceStrategy(traceStrategy tracer.Strategy) {
	b.traceStrategy = traceStrategy
}

func (b *Builder) buildLogger(configurations parser.Logging) error {
	output := io.Writer(os.Stdout)

	if configurations.LogsPath != "" {
		logsFile, err := os.OpenFile(configurations.LogsPath, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0666)
		if err != nil {
			return err
		}
		output = io.MultiWriter(os.Stdout, logsFile)
		b.logsFile = logsFile
	}

	log.SetOutput(output)

	log.SetFormatter(&log.JSONFormatter{
		DisableTimestamp: configurations.LogDisableTimestamp,
	})
	log.SetReportCaller(configurations.DebugReportCaller)
	if configurations.Debug {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	return nil
}

func (b *Builder) buildRabbitMQ(rabbitMQURI string) error {
	rabbitMQConnection, err := amqpDial(rabbitMQURI)
	if err != nil {
		return err
	}

	b.rabbitMQChannel, err = rabbitMQConnection.Channel()
	if err != nil {
		rabbitMQConnection.Close()
		return err
	}

	if _, err = b.rabbitMQChannel.QueueDeclare(RabbitmqQueueName, false, false, false, false, nil); err != nil {
		rabbitMQConnection.Close()
		return err
	}

	b.rabbitMQConnection = rabbitMQConnection
	return nil
}

func (b *Builder) Close() error {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()

	if !b.closing.CompareAndSwap(false, true) {
		return nil
	}

	// Stop background service plugins first so their goroutines drain before
	// other teardown.
	if b.servicesCancel != nil {
		b.servicesCancel()
	}
	for _, svc := range b.startedServices {
		svc.Stop()
	}
	b.startedServices = nil
	b.servicesCancel = nil

	if b.serviceGroup != nil {
		if err := b.serviceGroup.StopAll(); err != nil {
			log.Errorf("error stopping protocol servers: %s", err.Error())
		}
	}

	if b.beelzebubCloud != nil {
		b.beelzebubCloud.Stop()
	}

	if b.prometheusServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := b.prometheusServer.Shutdown(ctx); err != nil {
			log.Errorf("error shutting down Prometheus server: %s", err.Error())
		}
		cancel()
		b.prometheusServer = nil
	}

	if b.logsFile != nil {
		if err := b.logsFile.Close(); err != nil {
			log.Errorf("error closing log file: %s", err.Error())
		}
	}

	var errs []error
	if b.rabbitMQChannel != nil {
		if err := amqpCloseChannel(b.rabbitMQChannel); err != nil {
			errs = append(errs, fmt.Errorf("error closing RabbitMQ channel: %w", err))
		} else {
			b.rabbitMQChannel = nil
		}
	}
	if b.rabbitMQConnection != nil {
		if err := amqpCloseConnection(b.rabbitMQConnection); err != nil {
			errs = append(errs, fmt.Errorf("error closing RabbitMQ connection: %w", err))
		} else {
			b.rabbitMQConnection = nil
		}
	}
	if len(errs) > 0 {
		b.closing.Store(false)
		return errors.Join(errs...)
	}
	return nil
}

func (b *Builder) Run() error {
	if err := b.startPrometheus(); err != nil {
		return err
	}

	// Start registered background service plugins.
	svcCtx, cancel := context.WithCancel(context.Background())
	b.servicesCancel = cancel
	for _, svc := range plugin.Services() {
		if err := svc.Start(svcCtx); err != nil {
			log.Errorf("Error starting service plugin %q, continuing without it: %s",
				svc.Metadata().Name, err.Error())
			continue
		}
		b.startedServices = append(b.startedServices, svc)
	}

	// Init Protocol strategies via ServiceGroup
	b.serviceGroup = protocols.NewServiceGroup(b.traceStrategy)

	// Wire the deploy callback into the MCP strategy so tools can
	// deploy honeypot configurations at runtime.
	if mcpStrategy, ok := b.serviceGroup.StrategyForProtocol("mcp").(*MCP.MCPStrategy); ok {
		mcpStrategy.SetDeployFn(b.DeployService)
	}

	if b.beelzebubCoreConfigurations.Core.BeelzebubCloud.Enabled {
		conf := b.beelzebubCoreConfigurations.Core.BeelzebubCloud

		cloud := plugins.InitBeelzebubCloud(conf.URI, conf.AuthToken, b.handleCloudConfigChange, time.Duration(conf.PollingIntervalSeconds)*time.Second, nil)

		b.beelzebubCloud = cloud

		if honeypotsConfiguration, _, err := cloud.GetHoneypotsConfigurations(); err != nil {
			return err
		} else {
			b.beelzebubServicesConfiguration = honeypotsConfiguration
		}
	}

	if err := b.serviceGroup.InitServices(b.beelzebubServicesConfiguration); err != nil {
		return err
	}

	log.Infof("Started %d service(s)", len(b.beelzebubServicesConfiguration))
	return nil
}

func (b *Builder) handleCloudConfigChange(newConfigs []parser.BeelzebubServiceConfiguration, _ string) error {
	return b.Reload(newConfigs)
}

func (b *Builder) Reload(newConfigs []parser.BeelzebubServiceConfiguration) error {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()

	if b.closing.Load() {
		log.Warn("reload skipped: builder is closing")
		return nil
	}

	if b.serviceGroup == nil {
		return errors.New("reload called before Run()")
	}

	if err := b.serviceGroup.Reload(b.beelzebubServicesConfiguration, newConfigs); err != nil {
		return err
	}

	b.beelzebubServicesConfiguration = newConfigs
	return nil
}

func (b *Builder) DeployService(cfg parser.BeelzebubServiceConfiguration) error {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()

	if b.closing.Load() {
		return errors.New("builder is closing")
	}

	key := cfg.Protocol + ":" + cfg.Address
	for _, existing := range b.beelzebubServicesConfiguration {
		if existing.Protocol+":"+existing.Address == key {
			return fmt.Errorf("service %s already exists", key)
		}
	}

	strategy := b.serviceGroup.StrategyForProtocol(cfg.Protocol)
	if strategy == nil {
		return fmt.Errorf("protocol %q not supported", cfg.Protocol)
	}

	if err := b.serviceGroup.InitService(cfg, strategy); err != nil {
		return fmt.Errorf("failed to deploy %s: %w", key, err)
	}

	b.beelzebubServicesConfiguration = append(b.beelzebubServicesConfiguration, cfg)
	log.Infof("Deployed %s %s", strings.ToUpper(cfg.Protocol), cfg.Address)
	return nil
}

func (b *Builder) startPrometheus() error {
	config := b.beelzebubCoreConfigurations.Core.Prometheus
	if !config.Enabled || b.prometheusServer != nil {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(config.Path, promhttp.Handler())
	listener, err := net.Listen("tcp", config.Port)
	if err != nil {
		return fmt.Errorf("error init Prometheus: %w", err)
	}

	server := &http.Server{Addr: listener.Addr().String(), Handler: mux}
	b.prometheusServer = server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("Error serving Prometheus: %s", err.Error())
		}
	}()
	return nil
}

// build returns a degraded Builder for the initial startup phase.
// Only the configuration and tracing fields survive; the caller should
// use the original Builder (not the build copy) for Reload / Close.
func (b *Builder) build() *Builder {
	return &Builder{
		beelzebubServicesConfiguration: b.beelzebubServicesConfiguration,
		traceStrategy:                  b.traceStrategy,
		beelzebubCoreConfigurations:    b.beelzebubCoreConfigurations,
		prometheusServer:               b.prometheusServer,
	}
}

func NewBuilder() *Builder {
	return &Builder{}
}
