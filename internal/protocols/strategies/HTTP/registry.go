package HTTP

import "github.com/beelzebub-labs/beelzebub/v3/internal/protocols"

func init() {
	protocols.RegisterStrategy("http", func() protocols.ServiceStrategy { return &HTTPStrategy{} })
}
