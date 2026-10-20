package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

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

// eventChannel is the AMQP surface used for delivery; the real channel and
// test publishers share the same routing and resource-lifetime contract.
type eventChannel interface {
	QueueDeclare(string, bool, bool, bool, bool, amqp.Table) (amqp.Queue, error)
	PublishWithContext(context.Context, string, string, bool, bool, amqp.Publishing) error
	Close() error
}

type cloudEventSender interface {
	SendEvent(tracer.Event) (bool, error)
	Stop()
}

type Builder struct {
	beelzebubServicesConfiguration []parser.BeelzebubServiceConfiguration
	beelzebubCoreConfigurations    *parser.BeelzebubCoreConfigurations
	traceStrategy                  tracer.Strategy
	rabbitMQChannel                eventChannel
	rabbitMQConnection             *amqp.Connection
	logsFile                       *os.File
	startedServices                []plugin.ServicePlugin
	servicesCancel                 context.CancelFunc
	durableEvents                  *plugins.DurableEvents
	cloudSender                    cloudEventSender
	cloudPollingStop               func()
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

	b.rabbitMQChannel, err = rabbitMQConnection.Channel()
	if err != nil {
		return err
	}

	//creates a queue if it doesn't already exist, or ensures that an existing queue matches the same parameters.
	if _, err = b.rabbitMQChannel.QueueDeclare(RabbitmqQueueName, false, false, false, false, nil); err != nil {
		return err
	}

	b.rabbitMQConnection = rabbitMQConnection
	return nil
}

func (b *Builder) Close() error {
	if b.durableEvents != nil {
		b.durableEvents.Close()
	}
	if b.cloudSender != nil {
		b.cloudSender.Stop()
	}
	if b.cloudPollingStop != nil {
		b.cloudPollingStop()
	}

	// Stop background service plugins first so their goroutines drain before
	// other teardown.
	if b.servicesCancel != nil {
		b.servicesCancel()
	}
	for _, svc := range b.startedServices {
		svc.Stop()
	}

	// Close log file if it was opened
	if b.logsFile != nil {
		if err := b.logsFile.Close(); err != nil {
			return err
		}
	}

	// Close RabbitMQ connections
	if b.rabbitMQConnection != nil {
		if err := b.rabbitMQChannel.Close(); err != nil {
			return err
		}
		if err := b.rabbitMQConnection.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (b *Builder) Run() (runErr error) {
	if err := b.startDurableEvents(); err != nil {
		return err
	}

	defer func() {
		if runErr != nil && b.durableEvents != nil {
			b.durableEvents.Close()
		}
		if runErr != nil && b.cloudPollingStop != nil {
			b.cloudPollingStop()
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
	// Init Prometheus openmetrics
	go func() {
		if (b.beelzebubCoreConfigurations.Core.Prometheus != parser.Prometheus{}) {
			http.Handle(b.beelzebubCoreConfigurations.Core.Prometheus.Path, promhttp.Handler())

			if err := http.ListenAndServe(b.beelzebubCoreConfigurations.Core.Prometheus.Port, nil); err != nil {
				log.Fatalf("Error init Prometheus: %s", err.Error())
			}
		}
	}()

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
	if b.durableEvents != nil {
		protocolManager.SetTracer(tracer.NewDurableTracer(b.durableEvents.Enqueue))
	}

	if b.beelzebubCoreConfigurations.Core.BeelzebubCloud.Enabled {
		conf := b.beelzebubCoreConfigurations.Core.BeelzebubCloud

		beelzebubCloud := plugins.InitBeelzebubCloud(conf.URI, conf.AuthToken, true)
		b.cloudPollingStop = beelzebubCloud.Stop

		if honeypotsConfiguration, _, err := beelzebubCloud.GetHoneypotsConfigurations(); err != nil {
			return err
		} else {
			if len(honeypotsConfiguration) == 0 {
				return errors.New("no honeypots configuration found")
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
			log.Fatalf("protocol %s not managed", beelzebubServiceConfiguration.Protocol)
		}

		if err := protocolManager.InitService(beelzebubServiceConfiguration); err != nil {
			return fmt.Errorf("error during init protocol: %s, %s", beelzebubServiceConfiguration.Protocol, err.Error())
		}
	}

	return nil
}

func (b *Builder) startDurableEvents() error {
	conf := b.beelzebubCoreConfigurations.Core.BeelzebubCloud
	if !conf.Enabled || conf.EventSpoolDir == "" {
		return nil
	}

	delivery, err := plugins.NewDurableEvents(conf.URI, conf.AuthToken, conf.EventSpoolDir, conf.EventSpoolMaxBytes, conf.EventSpoolMaxRecords)

	if err != nil {
		return fmt.Errorf("opening durable event spool: %w", err)
	}

	b.durableEvents = delivery
	delivery.Start()
	return nil
}

func (b *Builder) build() *Builder {
	return &Builder{
		beelzebubServicesConfiguration: b.beelzebubServicesConfiguration,
		traceStrategy:                  b.traceStrategy,
		beelzebubCoreConfigurations:    b.beelzebubCoreConfigurations,
		cloudSender:                    b.cloudSender,
	}
}

func NewBuilder() *Builder {
	return &Builder{}
}
