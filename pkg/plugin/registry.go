package plugin

import (
	"fmt"
	"sort"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = make(map[string]Plugin)
)

// Register adds a plugin to the global registry.
// It panics if a plugin with the same name has already been registered.
// Call Register from an init() function so registration is automatic on import.
func Register(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	name := p.Metadata().Name
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("plugin %q already registered", name))
	}
	registry[name] = p
}

// Get retrieves a plugin by name.
func Get(name string) (Plugin, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[name]
	return p, ok
}

// GetCommand retrieves a CommandPlugin by name.
// Returns (nil, false) if the name is unknown or the plugin is not a CommandPlugin.
func GetCommand(name string) (CommandPlugin, bool) {
	p, ok := Get(name)
	if !ok {
		return nil, false
	}
	cp, ok := p.(CommandPlugin)
	return cp, ok
}

// GetHTTP retrieves an HTTPPlugin by name.
// Returns (nil, false) if the name is unknown or the plugin is not an HTTPPlugin.
func GetHTTP(name string) (HTTPPlugin, bool) {
	p, ok := Get(name)
	if !ok {
		return nil, false
	}
	hp, ok := p.(HTTPPlugin)
	return hp, ok
}

// Services returns sorted list of  all registered plugins that implement ServicePlugin
func Services() []ServicePlugin {
	mu.RLock()
	defer mu.RUnlock()
	var svcs []ServicePlugin
	for _, p := range registry {
		if sp, ok := p.(ServicePlugin); ok {
			svcs = append(svcs, sp)
		}
	}
	sort.Slice(svcs, func(i, j int) bool {
		return svcs[i].Metadata().Name < svcs[j].Metadata().Name
	})
	return svcs
}

// List returns the metadata for all registered plugins, sorted by name.
func List() []Metadata {
	mu.RLock()
	defer mu.RUnlock()
	list := make([]Metadata, 0, len(registry))
	for _, p := range registry {
		list = append(list, p.Metadata())
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}
