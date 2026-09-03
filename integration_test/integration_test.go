package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/builder"
	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/HTTP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/MCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/SSH"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TCP"
	_ "github.com/beelzebub-labs/beelzebub/v3/internal/protocols/strategies/TELNET"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/go-resty/resty/v2"
	"github.com/melbahja/goph"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/ssh"
)

type IntegrationTestSuite struct {
	suite.Suite
	beelzebubBuilder   *builder.Builder
	prometheusHost     string
	httpHoneypotHost   string
	tcpHoneypotHost    string
	sshHoneypotHost    string
	telnetHoneypotHost string
	rabbitMQURI        string

	tlsCertPath string
	tlsKeyPath  string
	tlsCleanup  func()
}

// readTelnetUntil reads from connection until it finds the expected string, skipping IAC sequences
func readTelnetUntil(conn *net.TCPConn, expected string) (string, error) {
	reply := make([]byte, 4096)
	var fullResponse string

	for {
		n, err := conn.Read(reply)
		if err != nil {
			return "", err
		}

		fullResponse += string(reply[:n])

		// Check if response contains expected string
		if strings.Contains(fullResponse, expected) {
			return fullResponse, nil
		}

		// If we've accumulated a lot of data without finding the expected string, something is wrong
		if len(fullResponse) > 8192 {
			return fullResponse, nil
		}
	}
}

func TestIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

func generateTLSCertInCertsDir(t *testing.T) (certPath, keyPath string, cleanup func()) {
	t.Helper()

	certsDir := filepath.Join(".", "certs")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("failed to create certs directory: %v", err)
	}

	certPath = filepath.Join(certsDir, "tls.crt")
	keyPath = filepath.Join(certsDir, "tls.key")

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{Organization: []string{"IntegrationTest"}},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("failed to create cert file: %v", err)
	}
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		_ = certFile.Close()
		t.Fatalf("failed to encode cert PEM: %v", err)
	}
	if err := certFile.Close(); err != nil {
		t.Fatalf("failed to close cert file: %v", err)
	}

	keyFile, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("failed to create key file: %v", err)
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}); err != nil {
		_ = keyFile.Close()
		t.Fatalf("failed to encode key PEM: %v", err)
	}
	if err := keyFile.Close(); err != nil {
		t.Fatalf("failed to close key file: %v", err)
	}

	cleanup = func() {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
	}

	return certPath, keyPath, cleanup
}

func (suite *IntegrationTestSuite) SetupSuite() {
	suite.T().Helper()
	if os.Getenv("INTEGRATION") == "" {
		suite.T().Skip("skipping integration tests, set environment variable INTEGRATION")
	}
	suite.httpHoneypotHost = "http://localhost:8080"
	suite.tcpHoneypotHost = "localhost:3306"
	suite.sshHoneypotHost = "localhost"
	suite.telnetHoneypotHost = "localhost:2300"
	suite.prometheusHost = "http://localhost:2112/metrics"

	suite.tlsCertPath, suite.tlsKeyPath, suite.tlsCleanup = generateTLSCertInCertsDir(suite.T())

	beelzebubConfigPath := "./configurations/beelzebub.yaml"
	servicesConfigDirectory := "./configurations/services/"

	parser := parser.Init(beelzebubConfigPath, servicesConfigDirectory)

	coreConfigurations, err := parser.ReadConfigurationsCore()
	suite.Require().NoError(err)
	suite.rabbitMQURI = coreConfigurations.Core.Tracings.RabbitMQ.URI

	beelzebubServicesConfiguration, err := parser.ReadConfigurationsServices()
	suite.Require().NoError(err)

	suite.beelzebubBuilder = builder.NewBuilder()

	director := builder.NewDirector(suite.beelzebubBuilder)

	suite.beelzebubBuilder, err = director.BuildBeelzebub(coreConfigurations, beelzebubServicesConfiguration)
	suite.Require().NoError(err)

	suite.Require().NoError(suite.beelzebubBuilder.Run())

	suite.Require().Eventually(func() bool {
		conn, err := net.DialTimeout("tcp", "localhost:8080", time.Second)

		if err != nil {
			return false
		}

		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "HTTP honeypot did not become ready in time")
}

func (suite *IntegrationTestSuite) TestInvokeHTTPHoneypot() {
	response, err := resty.New().R().
		Get(suite.httpHoneypotHost + "/index.php")

	response.Header().Del("Date")

	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, response.StatusCode())
	suite.Equal(http.Header{"Content-Length": []string{"15"}, "Content-Type": []string{"text/html"}, "Server": []string{"Apache/2.4.53 (Debian)"}, "X-Powered-By": []string{"PHP/7.4.29"}}, response.Header())
	suite.Equal("mocked response", string(response.Body()))

	response, err = resty.New().R().
		Get(suite.httpHoneypotHost + "/wp-admin")

	suite.Require().NoError(err)
	suite.Equal(http.StatusBadRequest, response.StatusCode())
	suite.Equal("mocked response", string(response.Body()))
}

func (suite *IntegrationTestSuite) TestInvokeTLSHoneypot() {
	type eventCollector struct {
		mu     sync.Mutex
		events []tracer.Event
	}
	var collector eventCollector

	// Store original tracer singleton strategy
	tr := tracer.GetInstance(nil)
	originalStrategy := tr.GetStrategy()

	wrapperStrategy := func(event tracer.Event) {
		// Wrap original tracer strategy to not lose functionalities
		if originalStrategy != nil {
			originalStrategy(event)
		}

		// fetch last event, it will be used to check for TLSServerName
		collector.mu.Lock()
		collector.events = append(collector.events, event)
		collector.mu.Unlock()
	}

	// update strategy with wrapper, set original strategy at function exit
	tr.SetStrategy(wrapperStrategy)
	defer tr.SetStrategy(originalStrategy)

	client := resty.New()
	client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})

	response, err := client.R().Get("https://localhost:8443/secure-index.php")
	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, response.StatusCode())
	suite.Equal("mocked secure response", string(response.Body()))
	headers := response.Header()
	suite.Equal("text/html", strings.TrimSpace(headers.Get("Content-Type")))
	suite.Equal("Apache/2.4.53 (Debian Secure)", strings.TrimSpace(headers.Get("Server")))
	suite.Equal("PHP/7.4.29 Secure", strings.TrimSpace(headers.Get("X-Powered-By")))

	response, err = client.R().Get("https://localhost:8443/secure-wp-admin")
	suite.Require().NoError(err)
	suite.Equal(http.StatusBadRequest, response.StatusCode())
	suite.Equal("mocked secure response", string(response.Body()))

	response, err = client.R().Get("https://localhost:8443/unknown-path")
	suite.Require().NoError(err)
	suite.Equal(http.StatusNotFound, response.StatusCode())
	suite.Equal("Secure not found!", string(response.Body()))

	// throttle cpu to wait event processing
	time.Sleep(100 * time.Millisecond)

	collector.mu.Lock()
	defer collector.mu.Unlock()
	found := false
	for _, ev := range collector.events {
		if ev.TLSServerName == "localhost" {
			found = true
			break
		}
	}
	suite.True(found, "Expected to find event with TLSServerName 'localhost'")
}

// TestHTTPTrustedProxyResolvesRealClient verifies that when the immediate TCP peer
// is in the configured trustedProxies list, X-Forwarded-For is honored end-to-end
// and the captured tracer event's SourceIp matches the leftmost untrusted XFF entry.
// XFF poisoning is also tested: a spoofed prefix followed by the legitimate client
// must surface the legitimate client, not the spoof.
func (suite *IntegrationTestSuite) TestHTTPTrustedProxyResolvesRealClient() {
	type eventCollector struct {
		mu     sync.Mutex
		events []tracer.Event
	}
	var collector eventCollector

	tr := tracer.GetInstance(nil)
	originalStrategy := tr.GetStrategy()
	wrapperStrategy := func(event tracer.Event) {
		if originalStrategy != nil {
			originalStrategy(event)
		}
		collector.mu.Lock()
		collector.events = append(collector.events, event)
		collector.mu.Unlock()
	}
	tr.SetStrategy(wrapperStrategy)
	defer tr.SetStrategy(originalStrategy)

	uri := suite.httpHoneypotHost + "/index.php?trusted-proxy-test=1"

	// Trusted peer (127.0.0.1) + spoofed prefix + legitimate client + inner trusted hop.
	// Walk-right-to-left should pick 8.8.8.8 (the first non-trusted entry from the right).
	_, err := resty.New().R().
		SetHeader("X-Forwarded-For", "1.1.1.1, 8.8.8.8, 127.0.0.1").
		Get(uri)
	suite.Require().NoError(err)

	// Throttle CPU to allow asynchronous event processing.
	time.Sleep(100 * time.Millisecond)

	collector.mu.Lock()
	defer collector.mu.Unlock()

	var matched *tracer.Event
	for i, ev := range collector.events {
		if strings.Contains(ev.RequestURI, "trusted-proxy-test=1") {
			matched = &collector.events[i]
			break
		}
	}
	suite.Require().NotNil(matched, "expected to capture a tracer event for the test request")
	suite.Equal("8.8.8.8", matched.SourceIp, "real client must be picked from XFF when peer is trusted")
	suite.Equal("8.8.8.8", matched.RemoteAddr, "RemoteAddr must reflect the resolved XFF client IP when peer is trusted")
}

func (suite *IntegrationTestSuite) TestInvokeTCPHoneypot() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.tcpHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	_, err = conn.Write([]byte("hello!"))
	suite.Require().NoError(err)

	reply := make([]byte, 1024)

	n, err := conn.Read(reply)
	suite.Require().NoError(err)

	suite.Equal("8.0.29\n", string(reply[:n]))
}

func (suite *IntegrationTestSuite) TestInvokeTCPInteractiveHoneypot() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", "localhost:6379")
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Test PING command
	_, err = conn.Write([]byte("PING\n"))
	suite.Require().NoError(err)

	reply := make([]byte, 1024)
	n, err := conn.Read(reply)
	suite.Require().NoError(err)
	suite.Equal("+PONG\r\n", string(reply[:n]))

	// Test INFO command
	_, err = conn.Write([]byte("INFO\n"))
	suite.Require().NoError(err)

	n, err = conn.Read(reply)
	suite.Require().NoError(err)
	suite.Equal("+INFO OK\r\n", string(reply[:n]))

	// Test unknown command
	_, err = conn.Write([]byte("UNKNOWN\n"))
	suite.Require().NoError(err)

	n, err = conn.Read(reply)
	suite.Require().NoError(err)
	suite.Equal("-ERR unknown command\r\n", string(reply[:n]))
}

func (suite *IntegrationTestSuite) TestInvokeSSHHoneypot() {
	client, err := goph.NewConn(
		&goph.Config{
			User:     "root",
			Addr:     suite.sshHoneypotHost,
			Port:     2222,
			Auth:     goph.Password("root"),
			Callback: ssh.InsecureIgnoreHostKey(),
		})
	suite.Require().NoError(err)
	defer client.Close()

	out, err := client.Run("")
	suite.Require().NoError(err)

	suite.Equal("root@ubuntu:~$ ", string(out))
}

func (suite *IntegrationTestSuite) TestInvokeTelnetHoneypot() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.telnetHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	// Set read/write timeout
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Read login prompt and send username
	response, err := readTelnetUntil(conn, "login:")
	suite.Require().NoError(err)
	suite.Contains(response, "login:")

	_, err = conn.Write([]byte("root\r\n"))
	suite.Require().NoError(err)

	// Read password prompt and send password
	response, err = readTelnetUntil(conn, "Password:")
	suite.Require().NoError(err)
	suite.Contains(response, "Password:")

	_, err = conn.Write([]byte("test\r\n"))
	suite.Require().NoError(err)

	// Read initial prompt
	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "root@test-server:~$")

	// Send a command
	_, err = conn.Write([]byte("whoami\r\n"))
	suite.Require().NoError(err)

	// Read response
	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "root")

	// Send exit command
	_, err = conn.Write([]byte("exit\r\n"))
	suite.Require().NoError(err)
}

func (suite *IntegrationTestSuite) TestTelnetAuthenticationFailure() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.telnetHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	// Set read/write timeout
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Read login prompt
	response, err := readTelnetUntil(conn, "login:")
	suite.Require().NoError(err)
	suite.Contains(response, "login:")

	// Send invalid username
	_, err = conn.Write([]byte("invaliduser\r\n"))
	suite.Require().NoError(err)

	// Read password prompt
	response, err = readTelnetUntil(conn, "Password:")
	suite.Require().NoError(err)
	suite.Contains(response, "Password:")

	// Send invalid password
	_, err = conn.Write([]byte("invalidpass\r\n"))
	suite.Require().NoError(err)

	// Should receive login incorrect message
	response, err = readTelnetUntil(conn, "Login incorrect")
	suite.Require().NoError(err)
	suite.Contains(response, "Login incorrect")
}

func (suite *IntegrationTestSuite) TestTelnetCommandResponses() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.telnetHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	// Set read/write timeout
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Authenticate
	response, err := readTelnetUntil(conn, "login:")
	suite.Require().NoError(err)

	_, err = conn.Write([]byte("root\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "Password:")
	suite.Require().NoError(err)

	_, err = conn.Write([]byte("test\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)

	// Test pwd command
	_, err = conn.Write([]byte("pwd\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "/root")

	// Test id command
	_, err = conn.Write([]byte("id\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "uid=0(root)")

	// Test ls command
	_, err = conn.Write([]byte("ls\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "bin")
	suite.Contains(response, "boot")

	// Test uname command
	_, err = conn.Write([]byte("uname\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "Linux")

	// Send exit
	_, err = conn.Write([]byte("exit\r\n"))
	suite.Require().NoError(err)
}

func (suite *IntegrationTestSuite) TestTelnetUnknownCommand() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.telnetHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	// Set read/write timeout
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Authenticate
	response, err := readTelnetUntil(conn, "login:")
	suite.Require().NoError(err)

	_, err = conn.Write([]byte("root\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "Password:")
	suite.Require().NoError(err)

	_, err = conn.Write([]byte("test\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)

	// Send unknown command
	_, err = conn.Write([]byte("unknowncmd\r\n"))
	suite.Require().NoError(err)

	// Should receive command not found message
	response, err = readTelnetUntil(conn, "command not found")
	suite.Require().NoError(err)
	suite.Contains(response, "command not found")

	// Send exit
	_, err = conn.Write([]byte("exit\r\n"))
	suite.Require().NoError(err)
}

func (suite *IntegrationTestSuite) TestTelnetSequenceOfCommands() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.telnetHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	// Set read/write timeout
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Authenticate
	response, err := readTelnetUntil(conn, "login:")
	suite.Require().NoError(err)
	suite.Contains(response, "login:")

	_, err = conn.Write([]byte("root\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "Password:")
	suite.Require().NoError(err)
	suite.Contains(response, "Password:")

	_, err = conn.Write([]byte("test\r\n"))
	suite.Require().NoError(err)

	response, err = readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(response, "root@test-server:~$")

	// Execute multiple commands in sequence
	commands := []struct {
		cmd      string
		expected string
	}{
		{"pwd\r\n", "/root"},
		{"whoami\r\n", "root"},
		{"uname\r\n", "Linux"},
		{"id\r\n", "uid=0(root)"},
	}

	for _, cmdTest := range commands {
		_, err := conn.Write([]byte(cmdTest.cmd))
		suite.Require().NoError(err)

		cmdResponse, err := readTelnetUntil(conn, "root@test-server:~$")
		suite.Require().NoError(err)
		suite.Contains(cmdResponse, cmdTest.expected, "Command: %s", cmdTest.cmd)
		suite.Contains(cmdResponse, "root@test-server:~$", "Expected prompt after command: %s", cmdTest.cmd)
	}

	// Send exit
	_, err = conn.Write([]byte("exit\r\n"))
	suite.Require().NoError(err)
}

func (suite *IntegrationTestSuite) TestTelnetPromptCorrectness() {
	tcpAddr, err := net.ResolveTCPAddr("tcp", suite.telnetHoneypotHost)
	suite.Require().NoError(err)

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	suite.Require().NoError(err)
	defer conn.Close()

	// Set read/write timeout
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Verify login prompt
	loginPrompt, err := readTelnetUntil(conn, "login:")
	suite.Require().NoError(err)
	suite.Contains(loginPrompt, "login:")

	// Send username
	_, err = conn.Write([]byte("root\r\n"))
	suite.Require().NoError(err)

	// Verify password prompt
	passwordPrompt, err := readTelnetUntil(conn, "Password:")
	suite.Require().NoError(err)
	suite.Contains(passwordPrompt, "Password:")

	// Send password
	_, err = conn.Write([]byte("test\r\n"))
	suite.Require().NoError(err)

	// Verify terminal prompt format
	terminalPrompt, err := readTelnetUntil(conn, "root@test-server:~$")
	suite.Require().NoError(err)
	suite.Contains(terminalPrompt, "root@test-server:~$")
	suite.NotContains(terminalPrompt, "login:")
	suite.NotContains(terminalPrompt, "Password:")

	// Send exit
	_, err = conn.Write([]byte("exit\r\n"))
	suite.Require().NoError(err)
}

func (suite *IntegrationTestSuite) TestRabbitMQ() {
	conn, err := amqp.Dial(suite.rabbitMQURI)
	suite.Require().NoError(err)
	defer conn.Close()

	ch, err := conn.Channel()
	suite.Require().NoError(err)
	defer ch.Close()

	for {
		msg, ok, err := ch.Get("event", true)
		suite.Require().NoError(err)

		if !ok || msg.Body == nil {
			break
		}
	}

	msgs, err := ch.Consume("event", "", true, false, false, false, nil)
	suite.Require().NoError(err)

	//Invoke HTTP Honeypot
	response, err := resty.New().R().Get(suite.httpHoneypotHost + "/index.php")

	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, response.StatusCode())
	timeout := time.After(5 * time.Second)

	for {
		select {
		case msg := <-msgs:
			var event tracer.Event
			err := json.Unmarshal(msg.Body, &event)
			suite.Require().NoError(err)

			if event.HTTPMethod == "" {
				continue
			}

			suite.Equal("GET", event.HTTPMethod)
			suite.Equal("/index.php", event.RequestURI)
			return

		case <-timeout:
			suite.Fail("timed out waiting for HTTP event in RabbitMQ queue")
			return
		}
	}
}
func (suite *IntegrationTestSuite) TestPrometheus() {
	//Invoke HTTP Honeypot
	response, err := resty.New().R().Get(suite.httpHoneypotHost + "/index.php")

	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, response.StatusCode())

	response, err = resty.New().R().Get(suite.prometheusHost)

	suite.Require().NoError(err)
	suite.Equal(http.StatusOK, response.StatusCode())
}

func (suite *IntegrationTestSuite) TearDownSuite() {
	suite.Require().NoError(suite.beelzebubBuilder.Close())
	if suite.tlsCleanup != nil {
		suite.tlsCleanup()
	}
}
