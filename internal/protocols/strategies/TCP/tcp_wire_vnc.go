package TCP

import (
	"encoding/hex"
	"fmt"
	"sync"
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

// vncChallengeStore maps sessionKey → 16-byte challenge sent to the client.
var vncChallengeStore sync.Map

type vncWirePlugin struct{}

func (vncWirePlugin) OnExchange(ctx *WireContext) {
	if ctx.Command == nil {
		return
	}
	switch ctx.Command.Name {
	case vncChallengeHandler:
		// The outbound response is the 16-byte challenge (already randomised
		// by the generic patch engine). Stash it for this session.
		if ctx.Response != nil && len(*ctx.Response) >= 16 {
			challenge := make([]byte, 16)
			copy(challenge, (*ctx.Response)[:16])
			vncChallengeStore.Store(ctx.SessionKey, challenge)
		}
	case vncResponseHandler:
		// The inbound request is the 16-byte DES-encrypted response.
		if len(ctx.Request) < 16 {
			return
		}
		v, ok := vncChallengeStore.Load(ctx.SessionKey)
		if !ok {
			return
		}
		challenge := v.([]byte)
		response := ctx.Request[:16]
		if ctx.Event.Metadata == nil {
			ctx.Event.Metadata = map[string]string{}
		}
		ctx.Event.Metadata["vnc_challenge"] = hex.EncodeToString(challenge)
		ctx.Event.Metadata["vnc_response"] = hex.EncodeToString(response)
		// John the Ripper VNC format (--format=vnc).
		ctx.Event.Metadata["vnc_john"] = fmt.Sprintf("$vnc$*%s*%s",
			hex.EncodeToString(challenge), hex.EncodeToString(response))
		vncChallengeStore.Delete(ctx.SessionKey)
	}
}

// OnSessionClose purges any stored challenge for the ended session, so an
// incomplete handshake (challenge sent, response never received) does not leak.
func (vncWirePlugin) OnSessionClose(sessionKey string) {
	vncChallengeStore.Delete(sessionKey)
}

func init() {
	RegisterWirePlugin(vncWirePlugin{})
}
