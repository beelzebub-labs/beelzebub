package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPluginCommands_ManagerCreationErrors(t *testing.T) {
	oldNew := newPluginManager
	t.Cleanup(func() { newPluginManager = oldNew })
	newPluginManager = func(string, string) (pluginManager, error) {
		return nil, errors.New("manager unavailable")
	}

	cmd, _ := commandWithOutput()
	if err := updatePlugin(cmd, nil); err == nil || err.Error() != "manager unavailable" {
		t.Fatalf("updatePlugin error = %v", err)
	}
	if err := removePlugin(cmd, []string{"scanner"}); err == nil || err.Error() != "manager unavailable" {
		t.Fatalf("removePlugin error = %v", err)
	}
}

func TestPaintColors(t *testing.T) {
	oldColorOn := colorOn
	colorOn = true
	t.Cleanup(func() { colorOn = oldColorOn })

	tests := []struct {
		name  string
		color string
		paint func(string) string
	}{
		{name: "green", color: cGreen, paint: green},
		{name: "blue", color: cBlue, paint: blue},
		{name: "cyan", color: cCyan, paint: cyan},
		{name: "purple", color: cPurple, paint: purple},
		{name: "dim", color: cDim, paint: dim},
		{name: "bold", color: cBold, paint: bold},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.paint("value")
			if !strings.Contains(got, tt.color) || !strings.HasSuffix(got, cReset) {
				t.Fatalf("painted value = %q, want color %q and reset", got, tt.color)
			}
		})
	}
}

func TestPaintWithoutColors(t *testing.T) {
	oldColorOn := colorOn
	colorOn = false
	t.Cleanup(func() { colorOn = oldColorOn })

	if got := blue("value"); got != "value" {
		t.Fatalf("blue without colors = %q, want value", got)
	}
}

func TestPrintInstalled_ConfigVariants(t *testing.T) {
	plugin := testLockedPlugin()
	plugin.ConfigPath = "/configurations/scanner.yaml"

	var out bytes.Buffer
	printInstalled(&out, plugin)
	if !strings.Contains(out.String(), "configurations/scanner.yaml") || !strings.Contains(out.String(), "(kept)") {
		t.Fatalf("kept plugin output = %q", out.String())
	}

	out.Reset()
	plugin.ConfigNew = true
	printInstalled(&out, plugin)
	if strings.Contains(out.String(), "(kept)") {
		t.Fatalf("new plugin output unexpectedly marked kept: %q", out.String())
	}
}
