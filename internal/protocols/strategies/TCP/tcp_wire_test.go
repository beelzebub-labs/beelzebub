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

// TestWirePlugin_SeamDispatch verifies the generic seam: registered wire-plugins
// run on each exchange and can mutate both the response bytes and the event.
// This exercises the extension point independently of any protocol.
func TestWirePlugin_SeamDispatch(t *testing.T) {
	saved := wirePlugins
	t.Cleanup(func() { wirePlugins = saved })
	wirePlugins = nil

	RegisterWirePlugin(mockWirePlugin{})

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
	saved := wirePlugins
	t.Cleanup(func() { wirePlugins = saved })
	wirePlugins = nil

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
