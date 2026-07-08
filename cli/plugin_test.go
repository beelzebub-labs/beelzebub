package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/beelzebub-labs/beelzebub/v3/internal/pluginmgr"
	"github.com/spf13/cobra"
)

type fakePluginManager struct {
	installSource string
	installRef    string
	installForce  bool
	declared      string
	applied       bool
	updateName    string
	removedName   string

	installResult         pluginmgr.LockedPlugin
	installErr            error
	installDeclaredResult []pluginmgr.LockedPlugin
	installDeclaredErr    error
	declareErr            error
	applyMode             string
	applyErr              error
	updateResult          []pluginmgr.UpdateResult
	updateErr             error
	removeResult          pluginmgr.LockedPlugin
	removeErr             error
	listResult            []pluginmgr.LockedPlugin
	listErr               error
}

func (f *fakePluginManager) Install(_ context.Context, rawSource, ref string, force bool) (pluginmgr.LockedPlugin, error) {
	f.installSource = rawSource
	f.installRef = ref
	f.installForce = force
	return f.installResult, f.installErr
}

func (f *fakePluginManager) InstallDeclared(_ context.Context, force bool) ([]pluginmgr.LockedPlugin, error) {
	f.installForce = force
	return f.installDeclaredResult, f.installDeclaredErr
}

func (f *fakePluginManager) Declare(source string) error {
	f.declared = source
	return f.declareErr
}

func (f *fakePluginManager) Apply(context.Context) (string, error) {
	f.applied = true
	return f.applyMode, f.applyErr
}

func (f *fakePluginManager) Update(_ context.Context, name string) ([]pluginmgr.UpdateResult, error) {
	f.updateName = name
	return f.updateResult, f.updateErr
}

func (f *fakePluginManager) Remove(_ context.Context, name string) (pluginmgr.LockedPlugin, error) {
	f.removedName = name
	return f.removeResult, f.removeErr
}

func (f *fakePluginManager) List() ([]pluginmgr.LockedPlugin, error) {
	return f.listResult, f.listErr
}

func withFakePluginManager(t *testing.T, mgr *fakePluginManager) {
	t.Helper()
	oldNew := newPluginManager
	oldRef := pluginInstallRef
	oldToken := pluginInstallToken
	oldForce := pluginInstallForce
	oldNoBuild := pluginInstallNoBuild
	oldUpdateToken := pluginUpdateToken

	newPluginManager = func(string, string) (pluginManager, error) {
		return mgr, nil
	}

	t.Cleanup(func() {
		newPluginManager = oldNew
		pluginInstallRef = oldRef
		pluginInstallToken = oldToken
		pluginInstallForce = oldForce
		pluginInstallNoBuild = oldNoBuild
		pluginUpdateToken = oldUpdateToken
	})
}

func commandWithOutput() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	return cmd, &out
}

func testLockedPlugin() pluginmgr.LockedPlugin {
	return pluginmgr.LockedPlugin{
		Name:    "scanner",
		Version: "1.2.3",
		Module:  "github.com/acme/scanner",
		Commit:  "cafebabecafebabecafebabecafebabecafebabe",
	}
}

func TestResolveToken(t *testing.T) {
	t.Setenv("BEELZEBUB_GITHUB_TOKEN", "env-token")

	if got := resolveToken("flag-token"); got != "flag-token" {
		t.Fatalf("resolveToken flag = %q, want flag-token", got)
	}

	if got := resolveToken(""); got != "env-token" {
		t.Fatalf("resolveToken env = %q, want env-token", got)
	}
}

func TestCalmExpectedErrors(t *testing.T) {
	var out bytes.Buffer

	if err := calm(&out, pluginmgr.ErrAlreadyInstalled); err != nil {
		t.Fatalf("calm returned expected error: %v", err)
	}

	if !strings.Contains(out.String(), "already installed") {
		t.Fatalf("calm output = %q", out.String())
	}

	errBoom := errors.New("boom")
	if err := calm(&out, errBoom); !errors.Is(err, errBoom) {
		t.Fatalf("calm unexpected error = %v, want %v", err, errBoom)
	}
}

func TestInstallPlugin_LinkNoBuild(t *testing.T) {
	mgr := &fakePluginManager{installResult: testLockedPlugin()}
	withFakePluginManager(t, mgr)
	pluginInstallRef = "v1.2.3"
	pluginInstallToken = "token"
	pluginInstallForce = true
	pluginInstallNoBuild = true

	cmd, out := commandWithOutput()
	err := installPlugin(cmd, []string{"github.com/acme/scanner"})
	if err != nil {
		t.Fatalf("installPlugin returned error: %v", err)
	}

	if mgr.installSource != "github.com/acme/scanner" || mgr.installRef != "v1.2.3" || !mgr.installForce {
		t.Fatalf("install args = %q %q %v", mgr.installSource, mgr.installRef, mgr.installForce)
	}

	if mgr.declared != "github.com/acme/scanner" {
		t.Fatalf("declared = %q", mgr.declared)
	}

	if mgr.applied {
		t.Fatal("Apply should not run with --no-build")
	}

	if !strings.Contains(out.String(), "Installed scanner") || !strings.Contains(out.String(), "make build") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestInstallPlugin_DeclaredAppliesDocker(t *testing.T) {
	mgr := &fakePluginManager{
		installDeclaredResult: []pluginmgr.LockedPlugin{testLockedPlugin()},
		applyMode:             "docker",
	}

	withFakePluginManager(t, mgr)
	cmd, out := commandWithOutput()
	err := installPlugin(cmd, nil)

	if err != nil {
		t.Fatalf("installPlugin declared returned error: %v", err)
	}

	if !mgr.applied {
		t.Fatal("Apply was not called")
	}

	if !strings.Contains(out.String(), "Rebuilt the image") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestInstallPlugin_AlreadyInstalledIsCalm(t *testing.T) {
	mgr := &fakePluginManager{installErr: pluginmgr.ErrAlreadyInstalled}
	withFakePluginManager(t, mgr)

	cmd, out := commandWithOutput()
	err := installPlugin(cmd, []string{"github.com/acme/scanner"})

	if err != nil {
		t.Fatalf("installPlugin returned error: %v", err)
	}

	if mgr.declared != "" {
		t.Fatalf("Declare should not run after already installed error, got %q", mgr.declared)
	}

	if !strings.Contains(out.String(), "already installed") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestUpdatePlugin(t *testing.T) {
	mgr := &fakePluginManager{updateResult: []pluginmgr.UpdateResult{
		{Name: "scanner", OldCommit: "aaaaaaaa", NewCommit: "bbbbbbbb", Changed: true},
	}}

	withFakePluginManager(t, mgr)
	pluginUpdateToken = "token"

	cmd, out := commandWithOutput()
	err := updatePlugin(cmd, []string{"scanner"})

	if err != nil {
		t.Fatalf("updatePlugin returned error: %v", err)
	}

	if mgr.updateName != "scanner" {
		t.Fatalf("updateName = %q", mgr.updateName)
	}

	if !strings.Contains(out.String(), "Updated scanner") || !strings.Contains(out.String(), "make build") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRemovePlugin(t *testing.T) {
	mgr := &fakePluginManager{removeResult: testLockedPlugin()}
	withFakePluginManager(t, mgr)

	cmd, out := commandWithOutput()
	err := removePlugin(cmd, []string{"scanner"})

	if err != nil {
		t.Fatalf("removePlugin returned error: %v", err)
	}

	if mgr.removedName != "scanner" {
		t.Fatalf("removedName = %q", mgr.removedName)
	}

	if !strings.Contains(out.String(), "Removed scanner") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestListPluginsInstalledFromLock(t *testing.T) {
	mgr := &fakePluginManager{listResult: []pluginmgr.LockedPlugin{testLockedPlugin()}}
	withFakePluginManager(t, mgr)

	cmd, out := commandWithOutput()
	listPlugins(cmd, nil)

	if !strings.Contains(out.String(), "Installed from git") || !strings.Contains(out.String(), "scanner") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestShortCommit(t *testing.T) {
	if got := shortCommit("cafebabecafebabe"); got != "cafebabe" {
		t.Fatalf("shortCommit long = %q", got)
	}

	if got := shortCommit("abc"); got != "abc" {
		t.Fatalf("shortCommit short = %q", got)
	}
}

func TestMain(m *testing.M) {
	colorOn = false
	os.Exit(m.Run())
}
