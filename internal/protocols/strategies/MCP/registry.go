package MCP

import "github.com/beelzebub-labs/beelzebub/v3/internal/protocols"

func init() {
	protocols.RegisterStrategy("mcp", func() protocols.ServiceStrategy { return &MCPStrategy{} })
}
