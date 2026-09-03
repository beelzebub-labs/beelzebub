package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/beelzebub-labs/beelzebub/v3/internal/builder"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/spf13/cobra"
)

var (
	runMemLimitMiB int
	// testShutdownCh overrides the signal channel for testing. Never set in production.
	testShutdownCh <-chan os.Signal
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the honeypot services",
	Long:  "Start all honeypot services defined in the configuration directory.",
	RunE:  runBeelzebub,
}

func init() {
	runCmd.Flags().IntVarP(&runMemLimitMiB, "mem-limit-mib", "m", 100, "Memory limit in MiB (-1 to disable)")
}

func runBeelzebub(cmd *cobra.Command, _ []string) error {
	if runMemLimitMiB > 0 {
		debug.SetMemoryLimit(int64(runMemLimitMiB) * 1024 * 1024)
	}

	p := parser.Init(rootConfCore, rootConfServices)

	coreConfigurations, err := p.ReadConfigurationsCore()
	if err != nil {
		return fmt.Errorf("reading core config: %w", err)
	}

	beelzebubServicesConfiguration, err := p.ReadConfigurationsServices()
	if err != nil {
		return fmt.Errorf("reading services config: %w", err)
	}

	if len(beelzebubServicesConfiguration) == 0 && !coreConfigurations.Core.BeelzebubCloud.Enabled {
		return errors.New("no services configured: provide a services directory, set BEELZEBUB_SERVICES_CONFIG, or enable beelzebub-cloud")
	}

	if validationErr := validateRuntimeConfiguration(coreConfigurations, beelzebubServicesConfiguration); validationErr != nil {
		return validationErr
	}

	fmt.Println(
		`
██████  ███████ ███████ ██      ███████ ███████ ██████  ██    ██ ██████  
██   ██ ██      ██      ██         ███  ██      ██   ██ ██    ██ ██   ██ 
██████  █████   █████   ██        ███   █████   ██████  ██    ██ ██████  
██   ██ ██      ██      ██       ███    ██      ██   ██ ██    ██ ██   ██ 
██████  ███████ ███████ ███████ ███████ ███████ ██████   ██████  ██████  
Deception runtime framework, happy hacking!`)

	beelzebubBuilder := builder.NewBuilder()
	director := builder.NewDirector(beelzebubBuilder)

	beelzebubBuilder, err = director.BuildBeelzebub(coreConfigurations, beelzebubServicesConfiguration)
	if err != nil {
		return fmt.Errorf("building beelzebub: %w", err)
	}

	if err = beelzebubBuilder.Run(); err != nil {
		return fmt.Errorf("starting services: %w", err)
	}
	defer beelzebubBuilder.Close()

	sig := func() os.Signal {
		if testShutdownCh != nil {
			return <-testShutdownCh
		}
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		return <-quit
	}()
	fmt.Fprintf(cmd.OutOrStdout(), "\nReceived signal %s, shutting down...\n", sig)

	return nil
}

func validateRuntimeConfiguration(core *parser.BeelzebubCoreConfigurations, services []parser.BeelzebubServiceConfiguration) error {
	serviceResult := parser.Validate(services, nil)
	coreResult := parser.ValidateCore(core, rootConfCore)
	if serviceResult.TotalErrors == 0 && coreResult.TotalErrors == 0 {
		return nil
	}
	return fmt.Errorf("runtime configuration validation failed: %d service error(s), %d core error(s)", serviceResult.TotalErrors, coreResult.TotalErrors)
}
