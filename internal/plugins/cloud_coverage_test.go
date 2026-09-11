package plugins

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/go-resty/resty/v2"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/require"
)

func TestBeelzebubCloud_NetworkAndHeaderPaths(t *testing.T) {
	client := resty.New()
	cloud := InitBeelzebubCloud("http://127.0.0.1:1", "token", nil, time.Second, client)
	_, err := cloud.SendEvent(tracer.Event{Headers: "X-Test: value"})
	require.Error(t, err)

	_, _, err = cloud.GetHoneypotsConfigurations()
	require.Error(t, err)
}

func TestBeelzebubCloud_InitCallbackError(t *testing.T) {
	cloud := InitBeelzebubCloud("", "", func([]parser.BeelzebubServiceConfiguration, string) error {
		return errors.New("callback failed")
	}, time.Millisecond, resty.New())
	time.Sleep(10 * time.Millisecond)
	cloud.Stop()
}

func TestBeelzebubCloud_InvalidRemoteConfigurations(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	cases := []struct {
		name   string
		config string
		want   string
	}{
		{name: "invalid command regex", config: "apiVersion: v1\nprotocol: ssh\naddress: ':22'\npasswordRegex: '.*'\ncommands:\n  - regex: '['\n    handler: ok\n", want: "invalid regex"},
		{name: "invalid trusted proxy", config: "apiVersion: v1\nprotocol: ssh\naddress: ':22'\npasswordRegex: '.*'\ntrustedProxies: ['not-an-ip']\ncommands:\n  - regex: '.*'\n    handler: ok\n", want: "TrustedProxies"},
		{name: "schema validation", config: "apiVersion: v1\nprotocol: ftp\naddress: ':22'\n", want: "validation failed"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			httpmock.RegisterResponder("GET", "http://cloud.test/honeypots", func(*http.Request) (*http.Response, error) {
				return httpmock.NewJsonResponse(200, []HoneypotConfigResponseDTO{{Config: tt.config}})
			})
			cloud := InitBeelzebubCloud("http://cloud.test", "token", nil, time.Second, client)
			_, _, err := cloud.GetHoneypotsConfigurations()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
			httpmock.Reset()
		})
	}
}
