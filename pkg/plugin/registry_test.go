package plugin_test

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCommand struct{ name string }

func (s *stubCommand) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: s.name, Version: "1.0.0"}
}
func (s *stubCommand) Execute(_ context.Context, req plugin.CommandRequest) (string, error) {
	return "echo:" + req.Command, nil
}

type stubHTTP struct{ name string }

func (s *stubHTTP) Metadata() plugin.Metadata { return plugin.Metadata{Name: s.name, Version: "1.0.0"} }
func (s *stubHTTP) HandleHTTP(_ *http.Request) plugin.HTTPResponse {
	return plugin.HTTPResponse{StatusCode: 200, Body: "maze"}
}

type stubService struct{ name string }

func (s *stubService) Metadata() plugin.Metadata {
	return plugin.Metadata{Name: s.name, Version: "1.0.0"}
}
func (s *stubService) Start(_ context.Context) error { return nil }
func (s *stubService) Stop()                         {}

func TestRegister_Get(t *testing.T) {
	cmd := &stubCommand{name: "TestCmd_" + t.Name()}
	plugin.Register(cmd)

	got, ok := plugin.Get(cmd.name)
	require.True(t, ok)
	assert.Equal(t, cmd.name, got.Metadata().Name)
}

func TestGetCommand(t *testing.T) {
	cmd := &stubCommand{name: "TestGetCommand_" + t.Name()}
	plugin.Register(cmd)

	cp, ok := plugin.GetCommand(cmd.name)
	require.True(t, ok)

	result, err := cp.Execute(context.Background(), plugin.CommandRequest{Command: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "echo:hello", result)
}

func TestGetHTTP(t *testing.T) {
	h := &stubHTTP{name: "TestGetHTTP_" + t.Name()}
	plugin.Register(h)

	hp, ok := plugin.GetHTTP(h.name)
	require.True(t, ok)

	resp := hp.HandleHTTP(nil)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestGetCommand_WrongType(t *testing.T) {
	h := &stubHTTP{name: "TestWrongType_" + t.Name()}
	plugin.Register(h)

	_, ok := plugin.GetCommand(h.name)
	assert.False(t, ok, "HTTPPlugin should not be returned as CommandPlugin")
}

func TestGet_Unknown(t *testing.T) {
	_, ok := plugin.Get("nonexistent-plugin-xyz")
	assert.False(t, ok)
}

func TestList_Sorted(t *testing.T) {
	plugin.Register(&stubCommand{name: "ZZZ_test"})
	plugin.Register(&stubCommand{name: "AAA_test"})

	list := plugin.List()
	require.GreaterOrEqual(t, len(list), 2)

	for i := 1; i < len(list); i++ {
		assert.LessOrEqual(t, list[i-1].Name, list[i].Name, "List should be sorted by name")
	}
}

func TestGetHTTP_WrongType(t *testing.T) {
	// A CommandPlugin registered should not be returned as HTTPPlugin
	cmd := &stubCommand{name: "TestGetHTTP_WrongType_" + t.Name()}
	plugin.Register(cmd)

	_, ok := plugin.GetHTTP(cmd.name)
	assert.False(t, ok, "CommandPlugin should not be returned as HTTPPlugin")
}

func TestGetCommand_NotFound(t *testing.T) {
	_, ok := plugin.GetCommand("definitely-does-not-exist-abc123")
	assert.False(t, ok)
}

func TestGetHTTP_NotFound(t *testing.T) {
	_, ok := plugin.GetHTTP("definitely-does-not-exist-abc123")
	assert.False(t, ok)
}

func TestRegister_Duplicate_Panics(t *testing.T) {
	name := "DupPlugin_" + t.Name()
	plugin.Register(&stubCommand{name: name})

	assert.Panics(t, func() {
		plugin.Register(&stubCommand{name: name})
	})
}

func TestServices_FiltersAndSortsByName(t *testing.T) {
	nameA := "TestServices_A_" + t.Name()
	nameB := "TestServices_B_" + t.Name()
	baseline := serviceNames(plugin.Services())
	plugin.Register(&stubService{name: nameB})
	plugin.Register(&stubCommand{name: "TestServices_Command_" + t.Name()})
	plugin.Register(&stubService{name: nameA})

	expected := append(baseline, nameA, nameB)
	sort.Strings(expected)

	require.Equal(t, expected, serviceNames(plugin.Services()))
}

func TestCleanup_RemovesRegisteredPlugins(t *testing.T) {
	plugins := make([]plugin.Plugin, 0, len(plugin.List()))
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			return
		}
		for _, registered := range plugins {
			plugin.Register(registered)
		}
	})

	for _, metadata := range plugin.List() {
		registered, ok := plugin.Get(metadata.Name)
		require.True(t, ok)
		plugins = append(plugins, registered)
	}

	plugin.Cleanup()
	cleaned = true

	assert.Empty(t, plugin.List())
}

func serviceNames(services []plugin.ServicePlugin) []string {
	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Metadata().Name)
	}
	sort.Strings(names)
	return names
}
