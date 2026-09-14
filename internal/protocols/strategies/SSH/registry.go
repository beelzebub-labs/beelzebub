package SSH

import "github.com/beelzebub-labs/beelzebub/v3/internal/protocols"

func init() {
	protocols.RegisterStrategy("ssh", func() protocols.ServiceStrategy { return &SSHStrategy{} })
}
