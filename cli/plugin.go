package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/beelzebub-labs/beelzebub/v3/internal/pluginmgr"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	// Blank imports ensure built-in plugins self-register before the command runs.
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/spf13/cobra"
)

var (
	pluginInstallRef     string
	pluginInstallToken   string
	pluginInstallForce   bool
	pluginInstallNoBuild bool
	pluginUpdateToken    string
)

type pluginManager interface {
	Install(ctx context.Context, rawSource, ref string, force bool) (pluginmgr.LockedPlugin, error)
	InstallDeclared(ctx context.Context, force bool) ([]pluginmgr.LockedPlugin, error)
	Declare(source string) error
	Apply(ctx context.Context) (string, error)
	Update(ctx context.Context, name string) ([]pluginmgr.UpdateResult, error)
	Remove(ctx context.Context, name string) (pluginmgr.LockedPlugin, error)
	List() ([]pluginmgr.LockedPlugin, error)
}

var newPluginManager = func(coreVersion, token string) (pluginManager, error) {
	return pluginmgr.NewManager(coreVersion, token)
}

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage and inspect plugins",
}

var pluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed plugins and which are compiled into this binary",
	Run:   listPlugins,
}

var pluginInstallCmd = &cobra.Command{
	Use:   "install [github-link]",
	Short: "Install plugins (rebuild to compile them in)",
	Long: "Install a plugin from a git repository, or with no argument install every\n" +
		"plugin declared in " + pluginmgr.DeclaredConfigPath + ". Installing by link also adds it to that file.",
	Example: "  beelzebub plugin install github.com/acme/foo --token $GITHUB_TOKEN\n" +
		"  beelzebub plugin install    # install everything in " + pluginmgr.DeclaredConfigPath,
	Args: cobra.MaximumNArgs(1),
	RunE: installPlugin,
}

var pluginUpdateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Re-fetch installed plugins at their declared ref and re-pin (rebuild to apply)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  updatePlugin,
}

var pluginRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"uninstall", "rm"},
	Short:   "Remove an installed plugin",
	Args:    cobra.ExactArgs(1),
	RunE:    removePlugin,
}

func init() {
	pluginInstallCmd.Flags().StringVar(&pluginInstallRef, "ref", "", "Git tag, branch, or commit to install (defaults to the repo default branch)")
	pluginInstallCmd.Flags().StringVar(&pluginInstallToken, "token", "", "Git token for private HTTPS repos (or set BEELZEBUB_GITHUB_TOKEN)")
	pluginInstallCmd.Flags().BoolVar(&pluginInstallForce, "force", false, "Replace an existing install of the same plugin")
	pluginInstallCmd.Flags().BoolVar(&pluginInstallNoBuild, "no-build", false, "Wire the plugin in but skip the rebuild")
	pluginUpdateCmd.Flags().StringVar(&pluginUpdateToken, "token", "", "Git token for private HTTPS repos (or set BEELZEBUB_GITHUB_TOKEN)")

	pluginCmd.AddCommand(pluginListCmd)
	pluginCmd.AddCommand(pluginInstallCmd)
	pluginCmd.AddCommand(pluginUpdateCmd)
	pluginCmd.AddCommand(pluginRemoveCmd)
}

func resolveToken(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("BEELZEBUB_GITHUB_TOKEN")
}

func installPlugin(cmd *cobra.Command, args []string) error {
	mgr, err := newPluginManager(Version, resolveToken(pluginInstallToken))

	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// No argument: install everything declared in configurations/plugins.yaml.
	if len(args) == 0 {
		installed, err := mgr.InstallDeclared(cmd.Context(), pluginInstallForce)

		if err != nil {
			return err
		}

		if len(installed) == 0 {
			fmt.Fprintf(out, "Nothing to install — %s is empty or all plugins are already installed.\n", pluginmgr.DeclaredConfigPath)
			return nil
		}

		for _, p := range installed {
			printInstalled(out, p)
		}

		return finishInstall(cmd, mgr)
	}

	// A link: install it and record it in configurations/plugins.yaml.
	fmt.Fprintf(out, "%s Installing %s\n", purple("⏺"), args[0])
	locked, err := mgr.Install(cmd.Context(), args[0], pluginInstallRef, pluginInstallForce)

	if err != nil {
		return calm(out, err) // expected conditions print calmly and exit 0
	}

	if err := mgr.Declare(args[0]); err != nil {
		return err
	}

	printInstalled(out, locked)
	return finishInstall(cmd, mgr)
}

// calm renders expected, non-fatal conditions (already installed, not installed)
// as a quiet line and exits 0; real errors are returned to surface normally.
func calm(out io.Writer, err error) error {
	if errors.Is(err, pluginmgr.ErrAlreadyInstalled) || errors.Is(err, pluginmgr.ErrNotInstalled) {
		fmt.Fprintf(out, "%s %s\n", dim("•"), err)
		return nil
	}

	return err
}

// printInstalled writes a branded "✓ Installed …" line.
func printInstalled(out io.Writer, p pluginmgr.LockedPlugin) {
	fmt.Fprintf(out, "%s Installed %s %s %s\n",
		green("✓"), bold(p.Name), p.Version, dim("("+p.Module+" @ "+shortCommit(p.Commit)+")"))
}

// finishInstall applies the newly wired plugins to the deployment (local build,
// or Docker rebuild+restart), unless --no-build was given.
func finishInstall(cmd *cobra.Command, mgr pluginManager) error {
	out := cmd.OutOrStdout()

	if pluginInstallNoBuild {
		fmt.Fprintln(out, dim("  rebuild to compile in:  make build"))
		return nil
	}

	fmt.Fprintln(out, dim("  applying…"))
	mode, err := mgr.Apply(cmd.Context())

	if err != nil {
		return err
	}

	if mode == "docker" {
		fmt.Fprintf(out, "%s Rebuilt the image and restarted the container.\n", green("✓"))
	} else {
		fmt.Fprintf(out, "%s Built ./beelzebub %s\n", green("✓"), dim("— restart it to load the change"))
	}

	return nil
}

func updatePlugin(cmd *cobra.Command, args []string) error {
	mgr, err := newPluginManager(Version, resolveToken(pluginUpdateToken))

	if err != nil {
		return err
	}

	var name string
	if len(args) == 1 {
		name = args[0]
	}

	results, err := mgr.Update(cmd.Context(), name)
	if err != nil {
		return calm(cmd.OutOrStdout(), err)
	}

	out := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintln(out, "No installed plugins to update.")
		return nil
	}

	changed := 0
	for _, r := range results {
		if r.Changed {
			changed++
			fmt.Fprintf(out, "%s Updated %s  %s → %s\n", green("✓"), bold(r.Name), dim(shortCommit(r.OldCommit)), cyan(shortCommit(r.NewCommit)))
		} else {
			fmt.Fprintf(out, "%s %s already up to date %s\n", dim("•"), r.Name, dim("("+shortCommit(r.NewCommit)+")"))
		}
	}

	if changed > 0 {
		fmt.Fprintln(out, dim("  rebuild to apply:  make build"))
	}

	return nil
}

func removePlugin(cmd *cobra.Command, args []string) error {
	mgr, err := newPluginManager(Version, "")
	if err != nil {
		return err
	}

	locked, err := mgr.Remove(cmd.Context(), args[0])
	if err != nil {
		return calm(cmd.OutOrStdout(), err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s Removed %s\n", green("✓"), bold(locked.Name))
	fmt.Fprintln(out, dim("  rebuild to drop it from the binary:  make build"))
	return nil
}

func listPlugins(cmd *cobra.Command, _ []string) {
	out := cmd.OutOrStdout()

	// Compiled-in plugins: those actually registered in this binary right now.
	metas := plugin.List()

	// Installed plugins: recorded in the lockfile ( compiled in only after rebuild)
	var installed []pluginmgr.LockedPlugin
	if mgr, err := newPluginManager(Version, ""); err == nil {
		installed, _ = mgr.List()
	}

	if len(metas) == 0 && len(installed) == 0 {
		fmt.Fprintln(out, "No plugins registered or installed.")
		return
	}

	fmt.Fprintln(out, bold("Compiled into this binary:"))
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tVERSION\tAUTHOR\tDESCRIPTION")

	for _, m := range metas {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", m.Name, m.Version, m.Author, m.Description)
	}

	tw.Flush()

	if len(installed) > 0 {
		fmt.Fprintln(out, "\n"+bold("Installed from git")+dim(" (rebuild with `make build` to compile in):"))
		itw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(itw, "  NAME\tVERSION\tMODULE\tCOMMIT")

		for _, p := range installed {
			fmt.Fprintf(itw, "  %s\t%s\t%s\t%s\n", p.Name, p.Version, p.Module, shortCommit(p.Commit))
		}

		itw.Flush()
	}
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}

	return c
}
