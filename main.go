package main

import (
	"os"

	"github.com/beelzebub-labs/beelzebub/v3/cli"
	_ "github.com/beelzebub-labs/beelzebub/v3/plugins"
	log "github.com/sirupsen/logrus"
)

func main() {
	if err := cli.Execute(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}
