package main

import (
	"os"

	"github.com/beelzebub-labs/beelzebub/v3/cli"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/HTTP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/SSH"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TELNET"
	_ "github.com/beelzebub-labs/beelzebub/v3/plugins"
	log "github.com/sirupsen/logrus"
)

var exitProcess = os.Exit

func main() {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		persistEmbeddedPluginLockfile()
	}

	if err := cli.Execute(); err != nil {
		log.Error(err)
		exitProcess(1)
	}
}
