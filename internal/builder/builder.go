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
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TELNET"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/HTTP"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/SSH"
	"github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TCP"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
)

const RabbitmqQueueName = "event"

var errRabbitMQUnavailable = errors.New("RabbitMQ channel unavailable")

type Builder struct {
	beelzebubServicesConfiguration []parser.BeelzebubServiceConfiguration
	beelzebubCoreConfigurations    *parser.BeelzebubCoreConfigurations
	traceStrategy                  tracer.Strategy
	rabbitMQChannel                *amqp.Channel
	rabbitMQConnection             *amqp.Connection
	logsFile                       *os.File
	startedServices                []plugin.ServicePlugin
	tcpStrategy                    *TCP.TCPStrategy
	servicesCancel                 context.CancelFunc
	prometheusServer               *http.Server
	cloudWatcher                   interface{ Stop() }
	rabbitMu                       sync.RWMutex
	closeOnce                      sync.Once
	closeErr                       error
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
	rabbitMQConnection, err := amqp.Dial(rabbitMQURI)
	if err != nil {
		return err
	}

	rabbitMQChannel, err := rabbitMQConnection.Channel()
	if err != nil {
		_ = rabbitMQConnection.Close()
		return err
	}

	//creates a queue if it doesn't already exist, or ensures that an existing queue matches the same parameters.
	if _, err = rabbitMQChannel.QueueDeclare(RabbitmqQueueName, false, false, false, false, nil); err != nil {
		_ = rabbitMQChannel.Close()
		_ = rabbitMQConnection.Close()
		return err
	}

	b.rabbitMu.Lock()
	b.rabbitMQChannel = rabbitMQChannel
	b.rabbitMQConnection = rabbitMQConnection
	b.rabbitMu.Unlock()
	return nil
}

func (b *Builder) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.closeResources()
	})
	return b.closeErr
}

func (b *Builder) closeResources() error {
	var err error

	servicesCancel := b.servicesCancel
	b.servicesCancel = nil
	startedServices := b.startedServices
	b.startedServices = nil
	tcpStrategy := b.tcpStrategy
	b.tcpStrategy = nil
	prometheusServer := b.prometheusServer
	b.prometheusServer = nil
	logsFile := b.logsFile
	b.logsFile = nil
	cloudWatcher := b.cloudWatcher
	b.cloudWatcher = nil

	if cloudWatcher != nil {
		cloudWatcher.Stop()
	}

	// Drain TCP handlers before stopping service plugins. A plugin can implement
	// both ServicePlugin and WirePlugin, and its exchange hooks may still need the
	// resources owned by the service until every active connection has closed.
	if tcpStrategy != nil {
		if closeErr := tcpStrategy.Shutdown(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if servicesCancel != nil {
		servicesCancel()
	}
	for _, svc := range startedServices {
		svc.Stop()
	}
	if prometheusServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if closeErr := prometheusServer.Shutdown(ctx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		cancel()
	}

	// Close log file if it was opened
	if logsFile != nil {
		// The package logger is global. Detach it from the file before closing so
		// tracer events already queued during shutdown still have a valid sink.
		log.SetOutput(os.Stdout)
		if closeErr := logsFile.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}

	// Prevent a publish from racing a channel/connection close. The dedicated
	// RabbitMQ lock is never held while stopping unrelated runtime components.
	b.rabbitMu.Lock()
	rabbitMQChannel := b.rabbitMQChannel
	b.rabbitMQChannel = nil
	rabbitMQConnection := b.rabbitMQConnection
	b.rabbitMQConnection = nil
	if rabbitMQConnection != nil {
		if rabbitMQChannel != nil {
			if closeErr := rabbitMQChannel.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}
		if closeErr := rabbitMQConnection.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	b.rabbitMu.Unlock()
	return err
}

func (b *Builder) publishRabbitMQ(publishing amqp.Publishing) error {
	b.rabbitMu.RLock()
	defer b.rabbitMu.RUnlock()
	if b.rabbitMQChannel == nil {
		return errRabbitMQUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return b.rabbitMQChannel.PublishWithContext(ctx, "", RabbitmqQueueName, false, false, publishing)
}

func (b *Builder) Run() (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, b.Close())
		}
	}()

	fmt.Println(
		`
██████  ███████ ███████ ██      ███████ ███████ ██████  ██    ██ ██████  
██   ██ ██      ██      ██         ███  ██      ██   ██ ██    ██ ██   ██ 
██████  █████   █████   ██        ███   █████   ██████  ██    ██ ██████  
██   ██ ██      ██      ██       ███    ██      ██   ██ ██    ██ ██   ██ 
██████  ███████ ███████ ███████ ███████ ███████ ██████   ██████  ██████  
Deception runtime framework, happy hacking!`)
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

	// Init Protocol strategies
	secureShellStrategy := &SSH.SSHStrategy{}
	hypertextTransferProtocolStrategy := &HTTP.HTTPStrategy{}
	transmissionControlProtocolStrategy := &TCP.TCPStrategy{}
	modelContextProtocolStrategy := &MCP.MCPStrategy{}
	telnetStrategy := &TELNET.TelnetStrategy{}

	// Init Tracer strategies, and set the trace strategy default HTTP
	protocolManager := protocols.InitProtocolManager(b.traceStrategy, hypertextTransferProtocolStrategy)

	if b.beelzebubCoreConfigurations.Core.BeelzebubCloud.Enabled {
		conf := b.beelzebubCoreConfigurations.Core.BeelzebubCloud

		beelzebubCloud := plugins.InitBeelzebubCloud(conf.URI, conf.AuthToken, true)
		b.cloudWatcher = beelzebubCloud

		if honeypotsConfiguration, _, err := beelzebubCloud.GetHoneypotsConfigurations(); err != nil {
			return err
		} else {
			if len(honeypotsConfiguration) == 0 {
				return errors.New("no honeypots configuration found")
			}
			if err := validateCloudConfigurations(honeypotsConfiguration); err != nil {
				return err
			}
			b.beelzebubServicesConfiguration = honeypotsConfiguration
		}
	}

	for _, beelzebubServiceConfiguration := range b.beelzebubServicesConfiguration {
		switch beelzebubServiceConfiguration.Protocol {
		case "http":
			protocolManager.SetProtocolStrategy(hypertextTransferProtocolStrategy)
		case "ssh":
			protocolManager.SetProtocolStrategy(secureShellStrategy)
		case "tcp":
			protocolManager.SetProtocolStrategy(transmissionControlProtocolStrategy)
		case "mcp":
			protocolManager.SetProtocolStrategy(modelContextProtocolStrategy)
		case "telnet":
			protocolManager.SetProtocolStrategy(telnetStrategy)
		default:
			return fmt.Errorf("protocol %s not managed", beelzebubServiceConfiguration.Protocol)
		}

		if err := protocolManager.InitService(beelzebubServiceConfiguration); err != nil {
			return fmt.Errorf("error during init protocol %s: %w", beelzebubServiceConfiguration.Protocol, err)
		}
		if beelzebubServiceConfiguration.Protocol == "tcp" && b.tcpStrategy == nil {
			// Store only a strategy that has successfully opened at least one TCP
			// service. Close can then roll back later startup failures and handle
			// SIGINT/SIGTERM without implying lifecycle support for other protocols.
			b.tcpStrategy = transmissionControlProtocolStrategy
		}
	}

	return nil
}

func validateCloudConfigurations(configurations []parser.BeelzebubServiceConfiguration) error {
	result := parser.Validate(configurations, nil)
	if result.TotalErrors == 0 {
		return nil
	}

	messages := make([]string, 0, result.TotalErrors)
	for _, validationResult := range result.Results {
		for _, issue := range validationResult.Issues {
			if issue.Level == parser.LevelError {
				messages = append(messages, issue.Message)
			}
		}
	}
	return fmt.Errorf("invalid honeypot configuration from cloud: %s", strings.Join(messages, "; "))
}

func (b *Builder) startPrometheus() error {
	config := b.beelzebubCoreConfigurations.Core.Prometheus
	if config.Path == "" && config.Port == "" {
		return nil
	}
	listener, err := net.Listen("tcp", config.Port)
	if err != nil {
		return fmt.Errorf("starting Prometheus listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle(config.Path, promhttp.Handler())
	server := &http.Server{Handler: mux}
	b.prometheusServer = server
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Errorf("Prometheus server stopped: %v", serveErr)
		}
	}()
	return nil
}

func (b *Builder) build() *Builder {
	return b
}

func NewBuilder() *Builder {
	return &Builder{}
}
