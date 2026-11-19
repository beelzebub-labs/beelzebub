package TELNET

import "github.com/beelzebub-labs/beelzebub/v3/internal/protocols"

func init() {
	protocols.RegisterStrategy("telnet", func() protocols.ServiceStrategy { return &TelnetStrategy{} })
}
