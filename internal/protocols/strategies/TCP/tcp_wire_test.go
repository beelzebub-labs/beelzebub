package TCP

import (
	"context"
	"errors"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

type testWirePlugin struct {
	name     string
	onWire   func(*plugin.WireContext) error
	onClose  func(string) error
	canClose bool
}

func (p *testWirePlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: p.name, Version: "test"}
}

func (p *testWirePlugin) OnExchange(_ context.Context, exchange *plugin.WireContext) error {
	if p.onWire == nil {
		return nil
	}
	return p.onWire(exchange)
}

type testSessionWirePlugin struct{ *testWirePlugin }

func (p *testSessionWirePlugin) OnSessionClose(_ context.Context, connID string) error {
	if p.onClose == nil {
		return nil
	}
	return p.onClose(connID)
}

func registerTestWire(t *testing.T, p plugin.WirePlugin) string {
	t.Helper()
	name := "tcp-wire-" + t.Name()
	switch typed := p.(type) {
	case *testWirePlugin:
		typed.name = name
	case *testSessionWirePlugin:
		typed.name = name
	default:
		t.Fatalf("unsupported test plugin %T", p)
	}
	plugin.Register(p)
	return name
}

func TestWirePlugin_SeamDispatch(t *testing.T) {
	p := &testWirePlugin{onWire: func(exchange *plugin.WireContext) error {
		exchange.Response = []byte("rewritten")
		if exchange.Metadata == nil {
			exchange.Metadata = map[string]string{}
		}
		exchange.Metadata["mock"] = exchange.Command.Name
		return nil
	}}
	name := registerTestWire(t, p)
	exchange := &plugin.WireContext{
		Command:  plugin.WireCommand{Name: "demo"},
		Request:  []byte("req"),
		Response: []byte("original"),
	}

	runWirePlugins(context.Background(), []string{name}, exchange)

	if string(exchange.Response) != "rewritten" {
		t.Errorf("response = %q, want rewritten", exchange.Response)
	}
	if exchange.Metadata["mock"] != "demo" {
		t.Errorf("Metadata[mock] = %q, want demo", exchange.Metadata["mock"])
	}
}

func TestWirePlugin_EmptySelectionIsNoop(t *testing.T) {
	exchange := &plugin.WireContext{Response: []byte("unchanged")}
	runWirePlugins(context.Background(), nil, exchange)
	if string(exchange.Response) != "unchanged" || exchange.Metadata != nil {
		t.Fatalf("empty selection changed exchange: %#v", exchange)
	}
}

func TestWirePlugin_UsesConfigurationOrder(t *testing.T) {
	var ran []string
	a := &testWirePlugin{onWire: func(*plugin.WireContext) error {
		ran = append(ran, "a")
		return nil
	}}
	b := &testWirePlugin{onWire: func(*plugin.WireContext) error {
		ran = append(ran, "b")
		return nil
	}}
	nameA := registerTestWire(t, a)
	nameB := nameA + "-b"
	b.name = nameB
	plugin.Register(b)

	runWirePlugins(context.Background(), []string{nameB, nameA}, &plugin.WireContext{})
	if len(ran) != 2 || ran[0] != "b" || ran[1] != "a" {
		t.Fatalf("plugins ran in order %v, want [b a]", ran)
	}
}

func TestWirePlugin_SessionCloseRespectsSelection(t *testing.T) {
	var closed []string
	wanted := &testSessionWirePlugin{testWirePlugin: &testWirePlugin{onClose: func(connID string) error {
		closed = append(closed, connID)
		return nil
	}}}
	wantedName := registerTestWire(t, wanted)
	skipped := &testSessionWirePlugin{testWirePlugin: &testWirePlugin{onClose: func(string) error {
		closed = append(closed, "unexpected")
		return nil
	}}}
	skipped.name = wantedName + "-skipped"
	plugin.Register(skipped)

	closeWireSessions(context.Background(), "conn-1", []string{wantedName})
	if len(closed) != 1 || closed[0] != "conn-1" {
		t.Fatalf("session close calls = %v, want [conn-1]", closed)
	}
}

func TestWirePlugin_ErrorAndPanicAreIsolated(t *testing.T) {
	errPlugin := &testWirePlugin{onWire: func(exchange *plugin.WireContext) error {
		exchange.Response = []byte("error mutation")
		exchange.Metadata["error"] = "leaked"
		return errors.New("expected")
	}}
	errName := registerTestWire(t, errPlugin)
	panicPlugin := &testWirePlugin{onWire: func(exchange *plugin.WireContext) error {
		exchange.Response = []byte("panic mutation")
		exchange.Metadata["panic"] = "leaked"
		panic("expected")
	}}
	panicPlugin.name = errName + "-panic"
	plugin.Register(panicPlugin)
	ran := false
	after := &testWirePlugin{onWire: func(exchange *plugin.WireContext) error {
		ran = true
		if string(exchange.Response) != "original" || len(exchange.Metadata) != 0 {
			t.Fatalf("failed plugin mutations leaked into next hook: %#v", exchange)
		}
		return nil
	}}
	after.name = errName + "-after"
	plugin.Register(after)

	exchange := &plugin.WireContext{Request: []byte("request"), Response: []byte("original"), Metadata: map[string]string{}}
	runWirePlugins(context.Background(), []string{errName, panicPlugin.name, after.name}, exchange)
	if !ran {
		t.Fatal("plugin after failures did not run")
	}
	if string(exchange.Response) != "original" || len(exchange.Metadata) != 0 {
		t.Fatalf("failed plugin mutations were committed: %#v", exchange)
	}
}

func TestWirePlugin_ImmutableFieldsAndReturnedAliasesAreIsolated(t *testing.T) {
	response := []byte("rewritten")
	p := &testWirePlugin{onWire: func(exchange *plugin.WireContext) error {
		exchange.Request[0] = 'X'
		exchange.Command.Name = "changed"
		exchange.Response = response
		exchange.Metadata = map[string]string{"result": "ok"}
		return nil
	}}
	name := registerTestWire(t, p)
	exchange := &plugin.WireContext{
		Request:  []byte("request"),
		Response: []byte("original"),
		Command:  plugin.WireCommand{Name: "original"},
	}

	runWirePlugins(context.Background(), []string{name}, exchange)
	response[0] = 'X'
	if string(exchange.Request) != "request" || exchange.Command.Name != "original" {
		t.Fatalf("plugin changed immutable fields: %#v", exchange)
	}
	if string(exchange.Response) != "rewritten" || exchange.Metadata["result"] != "ok" {
		t.Fatalf("mutable fields were not committed: %#v", exchange)
	}
}
