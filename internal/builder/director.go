package builder

import (
	"context"
	"encoding/json"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"

	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
)

type Director struct {
	builder *Builder
}

func NewDirector(builder *Builder) *Director {
	return &Director{
		builder: builder,
	}
}

func (d *Director) BuildBeelzebub(beelzebubCoreConfigurations *parser.BeelzebubCoreConfigurations, beelzebubServicesConfiguration []parser.BeelzebubServiceConfiguration) (*Builder, error) {
	d.builder.beelzebubServicesConfiguration = beelzebubServicesConfiguration
	d.builder.beelzebubCoreConfigurations = beelzebubCoreConfigurations
	if err := d.builder.buildLogger(beelzebubCoreConfigurations.Core.Logging); err != nil {
		return nil, err
	}

	d.builder.setTraceStrategy(d.standardOutStrategy)

	if beelzebubCoreConfigurations.Core.Tracings.RabbitMQ.Enabled {
		d.builder.setTraceStrategy(d.rabbitMQTraceStrategy)
		err := d.builder.buildRabbitMQ(beelzebubCoreConfigurations.Core.Tracings.RabbitMQ.URI)
		if err != nil {
			return nil, err
		}
	}

	if beelzebubCoreConfigurations.Core.BeelzebubCloud.Enabled {
		d.builder.setTraceStrategy(d.beelzebubCloudStrategy)
	}

	return d.builder.build(), nil
}

func (d *Director) standardOutStrategy(event tracer.Event) {
	log.WithFields(log.Fields{
		"status": event.Status,
		"event":  event,
	}).Info("New Event")
}

func (d *Director) beelzebubCloudStrategy(event tracer.Event) {
	log.WithFields(log.Fields{
		"status": event.Status,
		"event":  event,
	}).Info("New Event")

	conf := d.builder.beelzebubCoreConfigurations.Core.BeelzebubCloud

	beelzebubCloud := plugins.InitBeelzebubCloud(conf.URI, conf.AuthToken, false)

	result, err := beelzebubCloud.SendEvent(event)
	if err != nil {
		log.Error(err.Error())
	} else {
		log.WithFields(log.Fields{
			"status": result,
			"event":  event,
		}).Debug("Event published on beelzebub cloud")
	}
}

func (d *Director) rabbitMQTraceStrategy(event tracer.Event) {
	log.WithFields(log.Fields{
		"status": event.Status,
		"event":  event,
	}).Info("New Event")

	eventJSON, err := json.Marshal(event)
	if err != nil {
		log.Error(err.Error())
		return
	}

	publishing := amqp.Publishing{ContentType: "application/json", Body: eventJSON}

	if err = d.builder.rabbitMQChannel.PublishWithContext(context.TODO(), "", RabbitmqQueueName, false, false, publishing); err != nil {
		log.Error(err.Error())
	} else {
		log.WithFields(log.Fields{
			"status": event.Status,
			"event":  event,
		}).Debug("Event published")
	}
}
