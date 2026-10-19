// Package tracer is responsible for tracing the events that occur in the honeypots
package tracer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
)

const Workers = 5

type Event struct {
	EventKey   string `json:",omitempty"`
	DateTime   string
	RemoteAddr string
	Protocol   string
	Command    string
	// CommandRaw is a hex-escaped (\xNN) rendering of the exact bytes the
	// client sent. It is populated only when Command's UTF-8 string form would
	// lose data (i.e. the input is not valid UTF-8, as happens with binary
	// protocols), so the original bytes remain recoverable downstream. Empty —
	// and omitted from JSON — for UTF-8 traffic, leaving existing events
	// unchanged.
	CommandRaw      string `json:",omitempty"`
	CommandOutput   string
	Status          string
	Msg             string
	ID              string
	Environ         string
	User            string
	Password        string
	Client          string
	Headers         string
	HeadersMap      map[string][]string
	Cookies         string
	UserAgent       string
	HostHTTPRequest string
	Body            string
	HTTPMethod      string
	RequestURI      string
	Description     string
	SourceIp        string
	SourcePort      string
	TLSServerName   string
	Handler         string
	// Metadata carries protocol-specific values contributed by plugins without
	// coupling the tracer to any wire protocol implementation.
	Metadata map[string]string `json:",omitempty"`
}

type (
	Protocol int
	Status   int
)

const (
	HTTP Protocol = iota
	SSH
	TCP
	MCP
	TELNET
)

func (protocol Protocol) String() string {
	return [...]string{"HTTP", "SSH", "TCP", "MCP", "TELNET"}[protocol]
}

// ProtocolFromString converts a lowercase protocol name to its Protocol constant.
func ProtocolFromString(s string) (Protocol, bool) {
	switch s {
	case "http":
		return HTTP, true
	case "ssh":
		return SSH, true
	case "tcp":
		return TCP, true
	case "mcp":
		return MCP, true
	case "telnet":
		return TELNET, true
	default:
		return 0, false
	}
}

const (
	Start Status = iota
	End
	Stateless
	Interaction
)

func (status Status) String() string {
	return [...]string{"Start", "End", "Stateless", "Interaction"}[status]
}

type Strategy func(event Event)

type Tracer interface {
	TraceEvent(event Event)
}

// ContextTracer extends legacy capture without requiring existing tracer
// implementations to change. Durable producers should bind their lifetime
// with WithContext before calling TraceEvent.
type ContextTracer interface {
	Tracer
	TraceEventContext(context.Context, Event) error
}

type contextualTracer struct {
	ctx     context.Context
	capture Tracer
}

func WithContext(ctx context.Context, capture Tracer) ContextTracer {
	if ctx == nil {
		ctx = context.Background()
	}
	return &contextualTracer{ctx: ctx, capture: capture}
}

// TraceFinalEvent gives timeout and teardown evidence a fresh, bounded chance
// to persist after the producer lifetime ends. Accepted delivery stays owned
// by the spool; closing the spool still cancels this capture immediately.
func TraceFinalEvent(ctx context.Context, capture Tracer, event Event) {
	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	WithContext(ctx, capture).TraceEvent(event)
}

func (t *contextualTracer) TraceEvent(event Event) {
	if err := t.TraceEventContext(t.ctx, event); err != nil {
		log.Error("Event was not accepted by capture; producer canceled or spool unavailable")
	}
}

func (t *contextualTracer) TraceEventContext(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if capture, ok := t.capture.(ContextTracer); ok {
		return capture.TraceEventContext(ctx, event)
	}

	t.capture.TraceEvent(event)
	return nil
}

type tracer struct {
	strategy          Strategy
	eventsChan        chan Event
	eventsTotal       prometheus.Counter
	eventsSSHTotal    prometheus.Counter
	eventsTCPTotal    prometheus.Counter
	eventsHTTPTotal   prometheus.Counter
	eventsMCPTotal    prometheus.Counter
	eventsTelnetTotal prometheus.Counter

	strategyMutex sync.RWMutex
	durableSink   func(context.Context, Event) error
}

var lock = &sync.Mutex{}
var singleton *tracer

func GetInstance(defaultStrategy Strategy) *tracer {
	lock.Lock()
	defer lock.Unlock()
	if singleton == nil {
		singleton = &tracer{
			strategy:   defaultStrategy,
			eventsChan: make(chan Event, Workers),
			eventsTotal: promauto.NewCounter(prometheus.CounterOpts{
				Namespace: "beelzebub",
				Name:      "events_total",
				Help:      "The total number of events",
			}),
			eventsSSHTotal: promauto.NewCounter(prometheus.CounterOpts{
				Namespace: "beelzebub",
				Name:      "ssh_events_total",
				Help:      "The total number of SSH events",
			}),
			eventsTCPTotal: promauto.NewCounter(prometheus.CounterOpts{
				Namespace: "beelzebub",
				Name:      "tcp_events_total",
				Help:      "The total number of TCP events",
			}),
			eventsHTTPTotal: promauto.NewCounter(prometheus.CounterOpts{
				Namespace: "beelzebub",
				Name:      "http_events_total",
				Help:      "The total number of HTTP events",
			}),
			eventsMCPTotal: promauto.NewCounter(prometheus.CounterOpts{
				Namespace: "beelzebub",
				Name:      "mcp_events_total",
				Help:      "The total number of MCP events",
			}),
			eventsTelnetTotal: promauto.NewCounter(prometheus.CounterOpts{
				Namespace: "beelzebub",
				Name:      "telnet_events_total",
				Help:      "The total number of TELNET events",
			}),
		}

		for i := 0; i < Workers; i++ {
			go func(i int) {
				log.Debug("Trace worker: ", i)
				for event := range singleton.eventsChan {
					singleton.GetStrategy()(event)
				}
			}(i)
		}
	}

	return singleton
}

func (tracer *tracer) SetStrategy(strategy Strategy) {
	tracer.strategyMutex.Lock()
	defer tracer.strategyMutex.Unlock()
	tracer.strategy = strategy
}

func (tracer *tracer) GetStrategy() Strategy {
	tracer.strategyMutex.RLock()
	defer tracer.strategyMutex.RUnlock()
	return tracer.strategy
}

func (tracer *tracer) TraceEvent(event Event) {
	if err := tracer.TraceEventContext(context.Background(), event); err != nil {
		log.Error("Event was not accepted by durable capture; check spool availability and shutdown state")
	}
}

// SetDurableSink installs synchronous capture before the volatile worker queue.
// A closed sink remains installed so shutdown cannot fall back to volatile delivery.
func (tracer *tracer) SetDurableSink(sink func(context.Context, Event) error) {
	tracer.strategyMutex.Lock()
	defer tracer.strategyMutex.Unlock()
	tracer.durableSink = sink
}

// NewDurableTracer shares runtime counters without changing the legacy singleton
// or starting another set of volatile workers.
func NewDurableTracer(sink func(context.Context, Event) error) Tracer {
	base := GetInstance(func(Event) {})
	return &tracer{
		durableSink:       sink,
		eventsTotal:       base.eventsTotal,
		eventsSSHTotal:    base.eventsSSHTotal,
		eventsTCPTotal:    base.eventsTCPTotal,
		eventsHTTPTotal:   base.eventsHTTPTotal,
		eventsMCPTotal:    base.eventsMCPTotal,
		eventsTelnetTotal: base.eventsTelnetTotal,
	}
}

func (tracer *tracer) TraceEventContext(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	tracer.strategyMutex.RLock()
	sink := tracer.durableSink
	tracer.strategyMutex.RUnlock()

	if sink != nil {
		if event.EventKey == "" {
			key, err := uuid.NewV7()
			if err != nil {
				return fmt.Errorf("generate durable event key: %w", err)
			}
			event.EventKey = key.String()
		}

		event.DateTime = time.Now().UTC().Format(time.RFC3339Nano)
		if err := sink(ctx, event); err != nil {
			return err
		}

		tracer.updatePrometheusCounters(event.Protocol)
		return nil
	}

	event.DateTime = time.Now().UTC().Format(time.RFC3339)

	select {
	case tracer.eventsChan <- event:
	case <-ctx.Done():
		return ctx.Err()
	}

	tracer.updatePrometheusCounters(event.Protocol)
	return nil
}

func (tracer *tracer) updatePrometheusCounters(protocol string) {
	switch protocol {
	case HTTP.String():
		tracer.eventsHTTPTotal.Inc()
	case SSH.String():
		tracer.eventsSSHTotal.Inc()
	case TCP.String():
		tracer.eventsTCPTotal.Inc()
	case MCP.String():
		tracer.eventsMCPTotal.Inc()
	case TELNET.String():
		tracer.eventsTelnetTotal.Inc()
	}
	tracer.eventsTotal.Inc()
}
