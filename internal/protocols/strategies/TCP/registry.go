package TCP

import "github.com/beelzebub-labs/beelzebub/v3/internal/protocols"

func init() {
	protocols.RegisterStrategy("tcp", func() protocols.ServiceStrategy { return &TCPStrategy{} })
}
