// Package tracer is responsible for tracing the events that occur in the honeypots
package tracer

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	log "github.com/sirupsen/logrus"
)

const Workers = 5

type Event struct {
	DateTime        string
	RemoteAddr      string
	Protocol        string
	Command         string
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
}

var lock = &sync.Mutex{}
var singleton *tracer

func GetInstance(defaultStrategy Strategy) *tracer {
	if singleton == nil {
		lock.Lock()
		defer lock.Unlock()
		// This is to prevent expensive lock operations every time the GetInstance method is called
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
						singleton.strategy(event)
					}
				}(i)
			}
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
	event.DateTime = time.Now().UTC().Format(time.RFC3339Nano)

	tracer.eventsChan <- event

	tracer.updatePrometheusCounters(event.Protocol)
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
