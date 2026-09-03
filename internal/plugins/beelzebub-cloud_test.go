package plugins

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/go-resty/resty/v2"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/assert"
)

func validSSHConfig(address string) string {
	return fmt.Sprintf("apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \"%s\"\nserverVersion: \"OpenSSH\"\npasswordRegex: \"^root$\"\ncommands:\n  - regex: \"^.*$\"\n    handler: \"ok\"\n", address)
}

func TestBuildSendEventFailValidation(t *testing.T) {
	beelzebubCloud := InitBeelzebubCloud("", "", nil, 0, nil)

	_, err := beelzebubCloud.SendEvent(tracer.Event{})

	assert.Equal(t, "authToken is empty", err.Error())
}

func TestBuildSendEventWithResults(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	// Given
	httpmock.RegisterResponder("POST", fmt.Sprintf("%s/events", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, &tracer.Event{})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	//When
	result, err := beelzebubCloud.SendEvent(tracer.Event{})

	//Then
	assert.Equal(t, true, result)
	assert.Nil(t, err)
}

func TestBuildSendEventErro(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081/events"

	// Given
	httpmock.RegisterResponder("POST", uri,
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(500, ""), nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	//When
	result, _ := beelzebubCloud.SendEvent(tracer.Event{})

	//Then
	assert.Equal(t, false, result)
}

func TestGetHoneypotsConfigurationsWithResults(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	// Given
	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{
					ID:      "123456",
					Config:  "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2222\"\ndescription: \"SSH interactive ChatGPT\"\ncommands:\n  - regex: \"^(.+)$\"\n    plugin: \"LLMHoneypot\"\nserverVersion: \"OpenSSH\"\nserverName: \"ubuntu\"\npasswordRegex: \"^(root|qwerty|Smoker666|123456|jenkins|minecraft|sinus|alex|postgres|Ly123456)$\"\ndeadlineTimeoutSeconds: 60\nplugin:\n  llmModel: \"gpt-4o\"\n  llmProvider: \"openai\"\n  openAISecretKey: \"1234\"\n",
					TokenID: "1234567",
				},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	//When
	result, configurationsHash, err := beelzebubCloud.GetHoneypotsConfigurations()

	//Then
	assert.Equal(t, &[]parser.BeelzebubServiceConfiguration{
		{
			ApiVersion:  "v1",
			Protocol:    "ssh",
			Address:     ":2222",
			Description: "SSH interactive ChatGPT",
			Commands: []parser.Command{
				{
					RegexStr: "^(.+)$",
					Regex:    regexp.MustCompile("^(.+)$"),
					Plugin:   "LLMHoneypot",
				},
			},
			ServerVersion:          "OpenSSH",
			ServerName:             "ubuntu",
			PasswordRegex:          "^(root|qwerty|Smoker666|123456|jenkins|minecraft|sinus|alex|postgres|Ly123456)$",
			DeadlineTimeoutSeconds: 60,
			Plugin: parser.Plugin{
				LLMModel:        "gpt-4o",
				LLMProvider:     "openai",
				OpenAISecretKey: "1234",
			},
			TrustedProxiesNets: []*net.IPNet{},
		},
	}, &result)
	assert.Equal(t, "a89d24772e4ba7af3fc180934916631ab0827e43a4c29ccec79fc80c34b2d8d3", configurationsHash)
	assert.Nil(t, err)
}

func TestGetHoneypotsConfigurationsWithErrorValidation(t *testing.T) {
	//Given
	beelzebubCloud := InitBeelzebubCloud("", "", nil, 0, nil)

	//When
	result, _, err := beelzebubCloud.GetHoneypotsConfigurations()

	//Then
	assert.Nil(t, result)
	assert.Equal(t, "authToken is empty", err.Error())
}

func TestGetHoneypotsConfigurationsWithErrorAPI(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	// Given
	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(500, ""), nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	//When
	result, _, err := beelzebubCloud.GetHoneypotsConfigurations()

	//Then
	assert.Nil(t, result)
	assert.Equal(t, "Response code: 500, error: ", err.Error())
}

func TestGetHoneypotsConfigurationsWithErrorUnmarshal(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	// Given
	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, "error")
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	//When
	result, _, err := beelzebubCloud.GetHoneypotsConfigurations()

	//Then
	assert.Nil(t, result)
	assert.Equal(t, "json: cannot unmarshal string into Go value of type []plugins.HoneypotConfigResponseDTO", err.Error())
}

func TestGetHoneypotsConfigurationsWithErrorDeserializeYaml(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	// Given
	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{
					ID:      "123456",
					Config:  "error",
					TokenID: "1234567",
				},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	//When
	result, _, err := beelzebubCloud.GetHoneypotsConfigurations()

	//Then
	assert.Nil(t, result)
	assert.Equal(t, "yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str `error` into parser.BeelzebubServiceConfiguration", err.Error())
}

func TestCheckConfigurationsChanged_FirstCall(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"
	config := validSSHConfig(":2222")

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{ID: "123456", Config: config, TokenID: "1234567"},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	_, newHash, changed, err := beelzebubCloud.checkConfigurationsChanged("")
	assert.Nil(t, err)
	assert.False(t, changed)
	assert.NotEmpty(t, newHash)
}

func TestCheckConfigurationsChanged_ConfigsChanged(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "http://localhost:8081"
	callCount := 0

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			callCount++
			config := validSSHConfig(":2222")
			if callCount > 1 {
				config = validSSHConfig(":2223")
			}
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{ID: "123456", Config: config, TokenID: "1234567"},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	_, firstHash, changed, err := beelzebubCloud.checkConfigurationsChanged("")
	assert.Nil(t, err)
	assert.False(t, changed)
	assert.NotEmpty(t, firstHash)

	_, secondHash, changed, err := beelzebubCloud.checkConfigurationsChanged(firstHash)
	assert.Nil(t, err)
	assert.True(t, changed)
	assert.NotEqual(t, firstHash, secondHash)
}

func TestCheckConfigurationsChanged_HTTPError(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(500, ""), nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	_, newHash, changed, err := beelzebubCloud.checkConfigurationsChanged("")
	assert.NotNil(t, err)
	assert.Empty(t, newHash)
	assert.False(t, changed)
}

func TestVerifyConfigurationsChanged_StopsOnDone(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())

	uri := "localhost:8081"
	callCount := 0

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			callCount++
			config := validSSHConfig(":2222")
			if callCount > 1 {
				config = validSSHConfig(":2223")
			}
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{ID: "123456", Config: config, TokenID: "1234567"},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	onChangeCalled := make(chan struct{}, 1)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", func(configs []parser.BeelzebubServiceConfiguration, hash string) error {
		onChangeCalled <- struct{}{}
		return nil
	}, 50*time.Millisecond, client)

	select {
	case <-onChangeCalled:
		assert.Greater(t, callCount, 1)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for onChange callback")
	}

	beelzebubCloud.Stop()

	// Verify the goroutine stops: no more callbacks after Stop()
	time.Sleep(100 * time.Millisecond)
	afterStop := callCount
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, afterStop, callCount, "goroutine should stop after Stop()")

	httpmock.DeactivateAndReset()
}

func TestVerifyConfigurationsChanged_RetriesHTTPError(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "http://localhost:8081"
	callCount := 0
	changed := make(chan struct{}, 1)

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return httpmock.NewStringResponse(500, "temporary failure"), nil
			}
			config := validSSHConfig(fmt.Sprintf(":%d", 2220+callCount))
			return httpmock.NewJsonResponse(200, []HoneypotConfigResponseDTO{{Config: config}})
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", func([]parser.BeelzebubServiceConfiguration, string) error {
		changed <- struct{}{}
		return nil
	}, 10*time.Millisecond, client)
	defer beelzebubCloud.Stop()

	select {
	case <-changed:
		assert.GreaterOrEqual(t, callCount, 3)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for retry callback")
	}
}

func TestVerifyConfigurationsChanged_RetriesFailedCallback(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	callCount := 0
	callbackCount := 0
	callbackDone := make(chan struct{}, 1)
	httpmock.RegisterResponder("GET", "http://localhost:8081/honeypots", func(req *http.Request) (*http.Response, error) {
		callCount++
		address := ":2222"
		if callCount > 1 {
			address = ":2223"
		}
		return httpmock.NewJsonResponse(200, []HoneypotConfigResponseDTO{{
			Config: validSSHConfig(address),
		}})
	})

	cloud := InitBeelzebubCloud("http://localhost:8081", "test-token", func([]parser.BeelzebubServiceConfiguration, string) error {
		callbackCount++
		if callbackCount == 1 {
			return errors.New("reload failed")
		}
		callbackDone <- struct{}{}
		return nil
	}, 10*time.Millisecond, client)
	defer cloud.Stop()

	select {
	case <-callbackDone:
		assert.Equal(t, 2, callbackCount)
		assert.GreaterOrEqual(t, callCount, 3)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for callback retry")
	}
}

func TestGetHoneypotsConfigurations_RejectsUnknownProtocol(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder("GET", "http://localhost:8081/honeypots", func(req *http.Request) (*http.Response, error) {
		return httpmock.NewJsonResponse(200, []HoneypotConfigResponseDTO{{
			Config: "apiVersion: \"v1\"\nprotocol: \"unknown\"\naddress: \":2222\"\n",
		}})
	})

	cloud := InitBeelzebubCloud("http://localhost:8081", "test-token", nil, 0, client)
	_, _, err := cloud.GetHoneypotsConfigurations()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestVerifyConfigurationsChanged_DetectsEmptyToNonEmpty(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	callCount := 0
	httpmock.RegisterResponder("GET", "http://localhost:8081/honeypots", func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			return httpmock.NewJsonResponse(200, []HoneypotConfigResponseDTO{})
		}
		return httpmock.NewJsonResponse(200, []HoneypotConfigResponseDTO{{
			Config: validSSHConfig(":2222"),
		}})
	})

	changed := make(chan struct{}, 1)
	cloud := InitBeelzebubCloud("http://localhost:8081", "test-token", func([]parser.BeelzebubServiceConfiguration, string) error {
		changed <- struct{}{}
		return nil
	}, 10*time.Millisecond, client)
	defer cloud.Stop()

	select {
	case <-changed:
		assert.GreaterOrEqual(t, callCount, 2)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for empty-to-non-empty callback")
	}
}

func TestVerifyConfigurationsChanged_StopsOnContextAtTopOfLoop(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{ID: "123456", Config: validSSHConfig(":2222"), TokenID: "1234567"},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", nil, 0, nil)
	beelzebubCloud.client = client

	// Stop before verifyConfigurationsChanged starts.
	// The goroutine hits the ctx.Done() check at the top of the first loop iteration.
	beelzebubCloud.Stop()

	done := make(chan error, 1)
	go func() {
		done <- beelzebubCloud.verifyConfigurationsChanged()
	}()

	select {
	case err := <-done:
		assert.Nil(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for goroutine to stop at top of loop")
	}
}

func TestMapToEventDTO_WithHeaders(t *testing.T) {
	event := tracer.Event{
		DateTime: "2025-05-01T16:18:13Z",
		Headers:  `[Key: Content-Type, values: application/json]`,
	}
	beelzebubCloud := InitBeelzebubCloud("localhost:8081", "sdjdnklfjndslkjanfk", nil, 0, nil)
	eventDTO, err := beelzebubCloud.mapToEventDTO(event)

	assert.Nil(t, err)
	assert.NotEmpty(t, eventDTO.Headers)
	assert.Contains(t, eventDTO.Headers, "Content-Type")
}

func TestMapToEventDTO(t *testing.T) {
	event := tracer.Event{
		DateTime:        "2025-05-01T16:18:13Z",
		RemoteAddr:      "1.1.1.1:12345",
		Protocol:        "SSH",
		Command:         "cd /tmp",
		CommandOutput:   "",
		Status:          "Interaction",
		Msg:             "New SSH Terminal Session",
		ID:              "4f104892-738f-47ac-950f-6afce1b742c7",
		Environ:         "qwerty",
		User:            "root",
		Password:        "root",
		Client:          "ssh",
		Cookies:         "qwerty",
		UserAgent:       "qwerty",
		HostHTTPRequest: "beelzebub-honeypot.com",
		Body:            "qwerty",
		HTTPMethod:      "GET",
		RequestURI:      "/qwerty",
		Description:     "qwerty",
		SourceIp:        "1.1.1.1",
		SourcePort:      "12345",
		TLSServerName:   "beelzebub-honeypot.com",
	}
	beelzebubCloud := InitBeelzebubCloud("localhost:8081", "sdjdnklfjndslkjanfk", nil, 0, nil)
	eventDTO, err := beelzebubCloud.mapToEventDTO(event)
	assert.Nil(t, err)

	assert.Equal(t, EventDTO{
		DateTime:        "2025-05-01T16:18:13Z",
		RemoteAddr:      "1.1.1.1:12345",
		Protocol:        "SSH",
		Command:         "cd /tmp",
		CommandOutput:   "",
		Status:          "Interaction",
		Msg:             "New SSH Terminal Session",
		ID:              "4f104892-738f-47ac-950f-6afce1b742c7",
		Environ:         "qwerty",
		User:            "root",
		Password:        "root",
		Client:          "ssh",
		Cookies:         "qwerty",
		UserAgent:       "qwerty",
		HostHTTPRequest: "beelzebub-honeypot.com",
		Body:            "qwerty",
		HTTPMethod:      "GET",
		RequestURI:      "/qwerty",
		Description:     "qwerty",
		SourceIp:        "1.1.1.1",
		SourcePort:      "12345",
		TLSServerName:   "beelzebub-honeypot.com",
	}, eventDTO)
}

func TestInitBeelzebubCloud_WithCustomClientAndInterval(t *testing.T) {
	client := resty.New()
	pollingInterval := 5 * time.Second

	bc := InitBeelzebubCloud("http://localhost:9999", "test-token", nil, pollingInterval, client)
	defer bc.Stop()

	assert.Equal(t, pollingInterval, bc.PollingInterval)
	assert.NotNil(t, bc.client)
}

func TestInitBeelzebubCloud_NilOnChange(t *testing.T) {
	bc := InitBeelzebubCloud("http://localhost:9999", "test-token", nil, 0, nil)
	defer bc.Stop()

	assert.Equal(t, 15*time.Second, bc.PollingInterval)
	assert.NotNil(t, bc.client)
	assert.Equal(t, "http://localhost:9999", bc.URI)
	assert.Equal(t, "test-token", bc.AuthToken)
}
