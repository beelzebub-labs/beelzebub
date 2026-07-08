package TCP

import (
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

// mockWirePlugin demonstrates the generic contract: a wire-plugin may rewrite
// the outbound response and enrich the trace event's Metadata.
type mockWirePlugin struct{}

func (mockWirePlugin) OnExchange(ctx *WireContext) {
	*ctx.Response = []byte("rewritten")
	if ctx.Event.Metadata == nil {
		ctx.Event.Metadata = map[string]string{}
	}
	ctx.Event.Metadata["mock"] = ctx.Command.Name
}

func setWirePluginsForTest(t *testing.T, plugins []registeredWirePlugin) {
	t.Helper()
	wirePluginsMu.Lock()
	saved := append([]registeredWirePlugin(nil), wirePlugins...)
	wirePlugins = plugins
	wirePluginsMu.Unlock()
	t.Cleanup(func() {
		wirePluginsMu.Lock()
		defer wirePluginsMu.Unlock()
		wirePlugins = saved
	})
}

// TestWirePlugin_SeamDispatch verifies the generic seam: registered wire-plugins
// run on each exchange and can mutate both the response bytes and the event.
// This exercises the extension point independently of any protocol.
func TestWirePlugin_SeamDispatch(t *testing.T) {
	setWirePluginsForTest(t, nil)

	RegisterWirePlugin("mock", mockWirePlugin{})

	resp := []byte("original")
	ev := &tracer.Event{}
	runWirePlugins(&WireContext{
		SessionKey: "s1",
		Command:    &parser.Command{Name: "demo"},
		Request:    []byte("req"),
		Response:   &resp,
		Event:      ev,
	})

	if string(resp) != "rewritten" {
		t.Errorf("response = %q, want \"rewritten\" (wire-plugin did not mutate response)", resp)
	}
	if ev.Metadata["mock"] != "demo" {
		t.Errorf("Metadata[mock] = %q, want \"demo\" (wire-plugin did not enrich event)", ev.Metadata["mock"])
	}
}

// TestWirePlugin_EmptyRegistryIsNoop verifies that with no wire-plugins
// registered the dispatch is a harmless no-op (the default public build).
func TestWirePlugin_EmptyRegistryIsNoop(t *testing.T) {
	setWirePluginsForTest(t, nil)

	resp := []byte("unchanged")
	ev := &tracer.Event{}
	runWirePlugins(&WireContext{
		SessionKey: "s2",
		Command:    &parser.Command{Name: "x"},
		Response:   &resp,
		Event:      ev,
	})

	if string(resp) != "unchanged" {
		t.Errorf("response mutated by empty registry: %q", resp)
	}
	if ev.Metadata != nil {
		t.Errorf("Metadata set by empty registry: %v", ev.Metadata)
	}
}

// funcWirePlugin adapts a func to the WirePlugin interface for tests.
type funcWirePlugin func(*WireContext)

func (f funcWirePlugin) OnExchange(ctx *WireContext) { f(ctx) }

// TestWirePlugin_PerServiceSelection verifies a service only runs the wire-plugins
// named in its config, and runs all of them when the list is empty.
func TestWirePlugin_PerServiceSelection(t *testing.T) {
	run := func(enabled []string) []string {
		var ran []string
		setWirePluginsForTest(t, []registeredWirePlugin{
			{name: "a", plugin: funcWirePlugin(func(*WireContext) { ran = append(ran, "a") })},
			{name: "b", plugin: funcWirePlugin(func(*WireContext) { ran = append(ran, "b") })},
		})
		resp := []byte("x")
		runWirePlugins(&WireContext{
			Command:       &parser.Command{Name: "c"},
			Response:      &resp,
			Event:         &tracer.Event{},
			ServiceConfig: parser.BeelzebubServiceConfiguration{WirePlugins: enabled},
		})
		return ran
	}

	if got := run([]string{"b"}); len(got) != 1 || got[0] != "b" {
		t.Errorf("selection [b] ran %v, want [b]", got)
	}
	if got := run(nil); len(got) != 2 {
		t.Errorf("empty selection ran %v, want both", got)
	}
}

// TestWirePlugin_SessionCloseRespectsSelection verifies OnSessionClose is only
// dispatched to plugins enabled for the service.
func TestWirePlugin_SessionCloseRespectsSelection(t *testing.T) {
	sa := &sessionAwarePlugin{}
	other := &sessionAwarePlugin{}
	setWirePluginsForTest(t, []registeredWirePlugin{
		{name: "wanted", plugin: sa},
		{name: "skip", plugin: other},
	})

	closeWireSessions("conn-1", []string{"wanted"})

	if len(sa.closed) != 1 || sa.closed[0] != "conn-1" {
		t.Errorf("enabled plugin closed = %v, want [conn-1]", sa.closed)
	}
	if len(other.closed) != 0 {
		t.Errorf("disabled plugin should not be closed, got %v", other.closed)
	}
}

// sessionAwarePlugin records OnSessionClose calls to verify the teardown hook.
type sessionAwarePlugin struct{ closed []string }

func (p *sessionAwarePlugin) OnExchange(_ *WireContext) {}
func (p *sessionAwarePlugin) OnSessionClose(sessionKey string) {
	p.closed = append(p.closed, sessionKey)
}

// TestWirePlugin_SessionClose verifies closeWireSessions calls OnSessionClose
// on SessionAware plugins (and is a harmless no-op for plain ones).
func TestWirePlugin_SessionClose(t *testing.T) {
	sa := &sessionAwarePlugin{}
	setWirePluginsForTest(t, []registeredWirePlugin{
		{name: "mock", plugin: mockWirePlugin{}}, // NOT SessionAware
		{name: "sa", plugin: sa},
	})

	closeWireSessions("TCP1.2.3.4", nil)

	if len(sa.closed) != 1 || sa.closed[0] != "TCP1.2.3.4" {
		t.Errorf("OnSessionClose calls = %v, want [TCP1.2.3.4]", sa.closed)
	}
}
