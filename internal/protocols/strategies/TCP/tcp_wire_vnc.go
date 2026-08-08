package TCP

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"
)

// VNC Authentication (RFB security type 2) is a DES challenge-response scheme:
// the server sends a 16-byte random challenge, and the client returns the
// challenge encrypted with the (bit-reversed) password as the DES key. Capturing
// the challenge/response pair lets the password be cracked offline.
//
// This wire-plugin demonstrates the WirePlugin seam on a non-Windows binary
// protocol already shipped with beelzebub (configurations/services/tcp-5900-vnc.yaml):
//   - On the "vnc_challenge" exchange it stores the 16-byte challenge the
//     random patch just generated (keyed by session).
//   - On the "vnc_auth_response" exchange it pairs the client's 16-byte response
//     with the stored challenge and records both on Event.Metadata, including a
//     John the Ripper "$vnc$*challenge*response" string.
//
// Handler names ("vnc_challenge", "vnc_auth_response") wire the plugin to the
// config deterministically, rather than relying on packet-length heuristics.
const (
	vncChallengeHandler = "vnc_challenge"
	vncResponseHandler  = "vnc_auth_response"
)

// vncChallengeStore maps ConnID → 16-byte challenge sent to the client. Keyed
// per-connection (not per-source) so concurrent VNC handshakes from the same
// client IP don't clobber each other's challenge.
var vncChallengeStore sync.Map

type vncWirePlugin struct{}

func (vncWirePlugin) Metadata() plugin.Metadata {
	return plugin.Metadata{
		Name:        "vnc",
		Version:     "1.0.0",
		Author:      "Beelzebub Labs",
		Description: "Captures RFB VNC challenge-response material from TCP exchanges.",
	}
}

func (vncWirePlugin) OnExchange(_ context.Context, ctx *plugin.WireContext) error {
	switch ctx.Command.Name {
	case vncChallengeHandler:
		// The outbound response is the 16-byte challenge (already randomised
		// by the generic patch engine). Stash it for this session.
		if len(ctx.Response) >= 16 {
			challenge := make([]byte, 16)
			copy(challenge, ctx.Response[:16])
			vncChallengeStore.Store(ctx.ConnID, challenge)
		}
	case vncResponseHandler:
		// The inbound request is the 16-byte DES-encrypted response.
		if len(ctx.Request) < 16 {
			return nil
		}
		v, ok := vncChallengeStore.Load(ctx.ConnID)
		if !ok {
			return nil
		}
		challenge, ok := v.([]byte)
		if !ok {
			return nil
		}
		response := ctx.Request[:16]
		if ctx.Metadata == nil {
			ctx.Metadata = map[string]string{}
		}
		ctx.Metadata["vnc_challenge"] = hex.EncodeToString(challenge)
		ctx.Metadata["vnc_response"] = hex.EncodeToString(response)
		// John the Ripper VNC format (--format=vnc).
		ctx.Metadata["vnc_john"] = fmt.Sprintf("$vnc$*%s*%s",
			hex.EncodeToString(challenge), hex.EncodeToString(response))
		vncChallengeStore.Delete(ctx.ConnID)
	}
	return nil
}

// OnSessionClose purges any stored challenge for the ended connection, so an
// incomplete handshake (challenge sent, response never received) does not leak.
func (vncWirePlugin) OnSessionClose(_ context.Context, connID string) error {
	vncChallengeStore.Delete(connID)
	return nil
}

func init() {
	plugin.Register(vncWirePlugin{})
}
