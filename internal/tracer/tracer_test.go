package tracer

import (
	"github.com/prometheus/client_golang/prometheus"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInit(t *testing.T) {
	mockStrategy := func(event Event) {}

	tracer := GetInstance(mockStrategy)

	assert.NotNil(t, tracer.strategy)
}

func TestTraceEvent(t *testing.T) {
	eventCalled := Event{}
	var wg sync.WaitGroup

	mockStrategy := func(event Event) {
		defer wg.Done()

		eventCalled = event
	}

	tracer := GetInstance(mockStrategy)

	tracer.strategy = mockStrategy

	wg.Add(1)
	tracer.TraceEvent(Event{
		ID:       "mockID",
		Protocol: HTTP.String(),
		Status:   Stateless.String(),
	})
	wg.Wait()

	assert.NotNil(t, eventCalled.ID)
	assert.Equal(t, "mockID", eventCalled.ID)
	assert.Equal(t, HTTP.String(), eventCalled.Protocol)
	assert.Equal(t, Stateless.String(), eventCalled.Status)
}

func TestSetStrategy(t *testing.T) {
	eventCalled := Event{}
	var wg sync.WaitGroup

	mockStrategy := func(event Event) {
		defer wg.Done()

		eventCalled = event
	}

	tracer := GetInstance(mockStrategy)

	tracer.SetStrategy(mockStrategy)

	wg.Add(1)
	tracer.TraceEvent(Event{
		ID:       "mockID",
		Protocol: HTTP.String(),
		Status:   Stateless.String(),
	})
	wg.Wait()

	assert.NotNil(t, eventCalled.ID)
	assert.Equal(t, "mockID", eventCalled.ID)
	assert.Equal(t, HTTP.String(), eventCalled.Protocol)
	assert.Equal(t, Stateless.String(), eventCalled.Status)
}

func TestStringStatus(t *testing.T) {
	assert.Equal(t, Start.String(), "Start")
	assert.Equal(t, End.String(), "End")
	assert.Equal(t, Stateless.String(), "Stateless")
	assert.Equal(t, Interaction.String(), "Interaction")
}

type mockCounter struct {
	prometheus.Metric
	prometheus.Collector
	count int
}

func (m *mockCounter) Inc() {
	m.count++
}

func (m *mockCounter) Add(f float64) {
	m.count += int(f)
}

func newTracerWithMockCounters(strategy Strategy) (*tracer, map[string]*mockCounter) {
	counters := map[string]*mockCounter{
		"total":  {},
		"ssh":    {},
		"tcp":    {},
		"http":   {},
		"mcp":    {},
		"telnet": {},
	}
	tr := &tracer{
		strategy:          strategy,
		eventsChan:        make(chan Event, Workers),
		eventsTotal:       counters["total"],
		eventsSSHTotal:    counters["ssh"],
		eventsTCPTotal:    counters["tcp"],
		eventsHTTPTotal:   counters["http"],
		eventsMCPTotal:    counters["mcp"],
		eventsTelnetTotal: counters["telnet"],
	}
	return tr, counters
}

func TestUpdatePrometheusCounters(t *testing.T) {
	tests := []struct {
		protocol   string
		counterKey string
	}{
		{SSH.String(), "ssh"},
		{HTTP.String(), "http"},
		{TCP.String(), "tcp"},
		{MCP.String(), "mcp"},
		{TELNET.String(), "telnet"},
	}

	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			tr, counters := newTracerWithMockCounters(func(event Event) {})

			tr.updatePrometheusCounters(tt.protocol)

			assert.Equal(t, 1, counters["total"].count, "total counter should be incremented")
			assert.Equal(t, 1, counters[tt.counterKey].count, "%s counter should be incremented", tt.protocol)

			for key, c := range counters {
				if key != "total" && key != tt.counterKey {
					assert.Equal(t, 0, c.count, "%s counter should not be incremented", key)
				}
			}
		})
	}
}

func TestUpdatePrometheusCounters_UnknownProtocol(t *testing.T) {
	tr, counters := newTracerWithMockCounters(func(event Event) {})

	tr.updatePrometheusCounters("ftp")

	assert.Equal(t, 1, counters["total"].count, "total should still be incremented for unknown protocol")
	assert.Equal(t, 0, counters["ssh"].count)
	assert.Equal(t, 0, counters["http"].count)
	assert.Equal(t, 0, counters["tcp"].count)
	assert.Equal(t, 0, counters["mcp"].count)
	assert.Equal(t, 0, counters["telnet"].count)
}

func TestGetStrategy(t *testing.T) {
	mockStrategy := func(event Event) {}

	tracer := GetInstance(mockStrategy)

	retrievedStrategy := tracer.GetStrategy()
	assert.NotNil(t, retrievedStrategy)
}

func TestProtocolFromString(t *testing.T) {
	tests := []struct {
		input    string
		expected Protocol
		ok       bool
	}{
		{"http", HTTP, true},
		{"ssh", SSH, true},
		{"tcp", TCP, true},
		{"mcp", MCP, true},
		{"telnet", TELNET, true},
		{"HTTP", 0, false},
		{"unknown", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := ProtocolFromString(tt.input)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.expected, got)
			}
		})
	}
}

func TestStringProtocol(t *testing.T) {
	assert.Equal(t, "HTTP", HTTP.String())
	assert.Equal(t, "SSH", SSH.String())
	assert.Equal(t, "TCP", TCP.String())
	assert.Equal(t, "MCP", MCP.String())
	assert.Equal(t, "TELNET", TELNET.String())
}

func TestSetGetStrategyConcurrency(t *testing.T) {
	tracer := GetInstance(func(event Event) {})

	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(2)

		go func(id int) {
			defer wg.Done()
			mockStrategy := func(event Event) {}
			tracer.SetStrategy(mockStrategy)
		}(i)

		go func(id int) {
			defer wg.Done()
			strategy := tracer.GetStrategy()
			assert.NotNil(t, strategy)
		}(i)
	}

	wg.Wait()
}
