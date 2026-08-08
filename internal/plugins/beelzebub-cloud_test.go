package plugins

import (
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

func TestBuildSendEventFailValidation(t *testing.T) {
	beelzebubCloud := InitBeelzebubCloud("", "", false)

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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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
					Config:  "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2222\"\ndescription: \"SSH interactive ChatGPT\"\ncommands:\n  - regex: \"^(.+)$\"\n    plugin: \"LLMHoneypot\"\nserverVersion: \"OpenSSH\"\nserverName: \"ubuntu\"\npasswordRegex: \"^(root|qwerty|Smoker666|123456|jenkins|minecraft|sinus|alex|postgres|Ly123456)$\"\ndeadlineTimeoutSeconds: 60\nplugin:\n  llmModel: \"gpt-4o\"\n  openAISecretKey: \"1234\"\n",
					TokenID: "1234567",
				},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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
				OpenAISecretKey: "1234",
			},
			TrustedProxiesNets: []*net.IPNet{},
		},
	}, &result)
	assert.Equal(t, "f488015ba743c96aca43462b2f798d700af9b3eb7cab401435a0c522da6f5abf", configurationsHash)
	assert.Nil(t, err)
}

func TestGetHoneypotsConfigurationsWithErrorValidation(t *testing.T) {
	//Given
	beelzebubCloud := InitBeelzebubCloud("", "", false)

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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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
	config := "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2222\"\ndeadlineTimeoutSeconds: 60\npasswordRegex: \"^root$\"\n"

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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
	beelzebubCloud.client = client

	newHash, changed, err := beelzebubCloud.checkConfigurationsChanged("")
	assert.Nil(t, err)
	assert.False(t, changed)
	assert.NotEmpty(t, newHash)
}

func TestCheckConfigurationsChanged_ConfigsChanged(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"
	callCount := 0

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			callCount++
			config := "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2222\"\ndeadlineTimeoutSeconds: 60\npasswordRegex: \"^root$\"\n"
			if callCount > 1 {
				config = "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2223\"\ndeadlineTimeoutSeconds: 60\npasswordRegex: \"^root$\"\n"
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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
	beelzebubCloud.client = client

	firstHash, changed, err := beelzebubCloud.checkConfigurationsChanged("")
	assert.Nil(t, err)
	assert.False(t, changed)
	assert.NotEmpty(t, firstHash)

	secondHash, changed, err := beelzebubCloud.checkConfigurationsChanged(firstHash)
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

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
	beelzebubCloud.client = client

	newHash, changed, err := beelzebubCloud.checkConfigurationsChanged("")
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
			config := "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2222\"\n"
			if callCount > 1 {
				config = "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2223\"\n"
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

	exitCalled := make(chan struct{}, 1)
	origExit := exitFunction
	defer func() { exitFunction = origExit }()
	exitFunction = func(c int) {
		exitCalled <- struct{}{}
	}

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
	beelzebubCloud.client = client
	beelzebubCloud.PollingInterval = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- beelzebubCloud.verifyConfigurationsChanged()
	}()

	select {
	case <-exitCalled:
		assert.Greater(t, callCount, 1)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for exitFunction")
	}

	exitFunction = func(c int) {}
	beelzebubCloud.Stop()
	httpmock.DeactivateAndReset()

	select {
	case err := <-done:
		assert.Nil(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for goroutine to stop")
	}
}

func TestVerifyConfigurationsChanged_ReturnsHTTPError(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewStringResponse(500, ""), nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
	beelzebubCloud.client = client
	beelzebubCloud.PollingInterval = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- beelzebubCloud.verifyConfigurationsChanged()
	}()

	select {
	case err := <-done:
		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "Response code: 500")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for goroutine to return error")
	}
}

func TestWatchConfigurations_RetainsBaselineAcrossTransientError(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"
	callCount := 0
	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 2 {
				return httpmock.NewStringResponse(500, "temporary"), nil
			}
			address := ":2222"
			if callCount >= 3 {
				address = ":2223"
			}
			return httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{{
				ID:      "123456",
				Config:  fmt.Sprintf("apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \"%s\"\n", address),
				TokenID: "1234567",
			}})
		},
	)

	exitCalled := make(chan struct{}, 1)
	origExit := exitFunction
	defer func() { exitFunction = origExit }()
	exitFunction = func(int) { exitCalled <- struct{}{} }

	cloud := InitBeelzebubCloud(uri, "token", false)
	cloud.client = client
	cloud.PollingInterval = 10 * time.Millisecond
	cloud.watchDone = make(chan struct{})
	go func() {
		defer close(cloud.watchDone)
		cloud.watchConfigurations()
	}()

	select {
	case <-exitCalled:
		assert.GreaterOrEqual(t, callCount, 3)
	case <-time.After(2 * time.Second):
		cloud.Stop()
		t.Fatal("configuration change after transient error was not detected")
	}
	cloud.Stop()
}

func TestVerifyConfigurationsChanged_StopsOnContextAtTopOfLoop(t *testing.T) {
	client := resty.New()
	httpmock.ActivateNonDefault(client.GetClient())
	defer httpmock.DeactivateAndReset()

	uri := "localhost:8081"

	httpmock.RegisterResponder("GET", fmt.Sprintf("%s/honeypots", uri),
		func(req *http.Request) (*http.Response, error) {
			resp, err := httpmock.NewJsonResponse(200, &[]HoneypotConfigResponseDTO{
				{ID: "123456", Config: "apiVersion: \"v1\"\nprotocol: \"ssh\"\naddress: \":2222\"\n", TokenID: "1234567"},
			})
			if err != nil {
				return httpmock.NewStringResponse(500, ""), nil
			}
			return resp, nil
		},
	)

	beelzebubCloud := InitBeelzebubCloud(uri, "sdjdnklfjndslkjanfk", false)
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
	beelzebubCloud := InitBeelzebubCloud("localhost:8081", "sdjdnklfjndslkjanfk", false)
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
	beelzebubCloud := InitBeelzebubCloud("localhost:8081", "sdjdnklfjndslkjanfk", false)
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
