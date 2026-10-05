package HTTP

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/plugins"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/beelzebub-labs/beelzebub/v3/pkg/plugin"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

type HTTPStrategy struct{}

type httpResponse struct {
	StatusCode int
	Headers    []string
	Body       string
}

func (httpStrategy HTTPStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	serverMux := http.NewServeMux()

	serverMux.HandleFunc("/", newHTTPHandler(servConf, tr))
	go func() {
		var err error
		// Launch a TLS supporting server if we are supplied a TLS Key and Certificate.
		// If relative paths are supplied, they are relative to the CWD of the binary.
		// The can be self-signed, only the client will validate this (or not).
		if servConf.TLSKeyPath != "" && servConf.TLSCertPath != "" {
			err = http.ListenAndServeTLS(servConf.Address, servConf.TLSCertPath, servConf.TLSKeyPath, serverMux)
		} else {
			err = http.ListenAndServe(servConf.Address, serverMux)
		}
		if err != nil {
			log.Errorf("error during init HTTP Protocol: %v", err)
			return
		}
	}()

	log.WithFields(log.Fields{
		"port":     servConf.Address,
		"commands": len(servConf.Commands),
	}).Infof("Init service: %s", servConf.Description)
	return nil
}

func newHTTPHandler(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		var resp httpResponse
		var err error
		command, allowedMethods := matchHTTPCommand(servConf.Commands, request)
		if command != nil {
			resp, err = buildHTTPResponse(servConf, tr, *command, request)
			if err != nil {
				log.Errorf("error building http response: %s: %v", request.RequestURI, err)
				resp.StatusCode = 500
				resp.Body = "500 Internal Server Error"
			}
		} else if len(allowedMethods) > 0 {
			resp.StatusCode = http.StatusMethodNotAllowed
			resp.Headers = []string{"Allow:" + strings.Join(allowedMethods, ", ")}
			resp.Body = http.StatusText(http.StatusMethodNotAllowed)
			body, meta := readRequestBody(request)
			addResponseDescriptors(meta, resp)
			traceRequest(request, tr, parser.Command{}, servConf.Description, body, meta, servConf.TrustedProxiesNets)
		} else {
			// If none of the main commands matched, and we have a fallback command configured, process it here.
			// The regexp is ignored for fallback commands, as they are catch-all for any request.
			command := servConf.FallbackCommand
			if command.Handler != "" || command.Plugin != "" {
				resp, err = buildHTTPResponse(servConf, tr, command, request)
				if err != nil {
					log.Errorf("error building http response: %s: %v", request.RequestURI, err)
					resp.StatusCode = 500
					resp.Body = "500 Internal Server Error"
				}
			}
		}
		setResponseHeaders(responseWriter, resp.Headers, resp.StatusCode)
		fmt.Fprint(responseWriter, resp.Body)
	}
}

func matchHTTPCommand(commands []parser.Command, request *http.Request) (*parser.Command, []string) {
	var allowedMethods []string
	for i := range commands {
		command := &commands[i]
		if !command.Regex.MatchString(request.RequestURI) {
			continue
		}
		if len(command.Methods) == 0 || slices.Contains(command.Methods, request.Method) {
			return command, nil
		}
		for _, method := range command.Methods {
			if !slices.Contains(allowedMethods, method) {
				allowedMethods = append(allowedMethods, method)
			}
		}
	}
	return nil, allowedMethods
}

func buildHTTPResponse(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, command parser.Command, request *http.Request) (resp httpResponse, err error) {
	resp = httpResponse{
		Body:       command.Handler,
		Headers:    command.Headers,
		StatusCode: command.StatusCode,
	}

	body, meta := readRequestBody(request)

	// The event is emitted once the response is known, so it can carry the
	// status and size actually decided for this request.
	defer func() {
		sent := resp
		if err != nil {
			// newHTTPHandler replaces a failed build with a 500; describe that.
			sent = httpResponse{StatusCode: 500, Body: "500 Internal Server Error"}
		}
		addResponseDescriptors(meta, sent)
		traceRequest(request, tr, command, servConf.Description, body, meta, servConf.TrustedProxiesNets)
	}()

	if command.Plugin != "" {
		host, _ := realClientAddr(request, servConf.TrustedProxiesNets)

		if cp, ok := plugin.GetCommand(command.Plugin); ok {
			cmd := fmt.Sprintf("Method: %s, RequestURI: %s, Body: %s", request.Method, request.RequestURI, body)
			output, err := cp.Execute(context.Background(), plugin.CommandRequest{
				Command:  cmd,
				ClientIP: host,
				Protocol: "http",
				Config:   plugins.ConfigFromServiceConf(servConf),
			})
			if err != nil {
				resp.Body = "404 Not Found!"
				return resp, fmt.Errorf("plugin %q execute error: %w", command.Plugin, err)
			}
			resp.Body = output
		} else if hp, ok := plugin.GetHTTP(command.Plugin); ok {
			// For HTTP-specific plugins (e.g. MazeHoneypot) that need full
			// request context and return their own status/headers.
			// ServerVersion and ServerName are injected here from service config.
			if command.Plugin == plugins.MazePluginName {
				maze := &plugins.MazeHoneypot{
					ServerVersion: servConf.ServerVersion,
					ServerName:    servConf.ServerName,
				}
				mazeResp := maze.HandleRequest(request)
				resp.StatusCode = mazeResp.StatusCode
				resp.Body = mazeResp.Body
				for k, v := range mazeResp.Headers {
					resp.Headers = append(resp.Headers, fmt.Sprintf("%s: %s", k, v))
				}
				resp.Headers = append(resp.Headers, fmt.Sprintf("Content-Type: %s", mazeResp.ContentType))
			} else {
				httpResp := hp.HandleHTTP(request)
				resp.StatusCode = httpResp.StatusCode
				resp.Body = httpResp.Body
				resp.Headers = append(resp.Headers, fmt.Sprintf("Content-Type: %s", httpResp.ContentType))
				for k, v := range httpResp.Headers {
					resp.Headers = append(resp.Headers, fmt.Sprintf("%s: %s", k, v))
				}
			}
		} else {
			log.Warnf("unknown plugin %q, skipping", command.Plugin)
		}
	}

	return resp, nil
}

// maxRequestBodyBytes bounds how much of a request body is read and traced.
const maxRequestBodyBytes = 1024 * 1024

// readRequestBody reads at most maxRequestBodyBytes of the body and reports,
// through Metadata descriptors, how many bytes were kept and whether the body
// was longer than that. One byte past the limit is read only to learn whether
// more existed; it is not retained.
//
// Descriptor keys use the reserved "body." domain. Metadata keys are
// lower-case and dot-namespaced by domain so plugins and strategies cannot
// collide; see the key convention documented alongside Event.Metadata.
func readRequestBody(request *http.Request) (string, map[string]string) {
	meta := map[string]string{}
	if request.Body == nil {
		meta["body.request.size"] = "0"
		meta["body.request.truncated"] = "false"
		meta["body.request.read_error"] = "false"
		return "", meta
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
	truncated := len(bodyBytes) > maxRequestBodyBytes
	if truncated {
		bodyBytes = bodyBytes[:maxRequestBodyBytes]
	}
	meta["body.request.size"] = strconv.Itoa(len(bodyBytes))
	meta["body.request.truncated"] = strconv.FormatBool(truncated)
	meta["body.request.read_error"] = strconv.FormatBool(err != nil)
	return string(bodyBytes), meta
}

// addResponseDescriptors records the status and body size of the response as
// built for this request. The size is what the handler produced, not bytes
// confirmed on the wire. Status uses the reserved "http." domain, size the
// "body." domain.
func addResponseDescriptors(meta map[string]string, resp httpResponse) {
	if meta == nil {
		return
	}
	meta["http.response.status"] = strconv.Itoa(resp.StatusCode)
	meta["body.response.size"] = strconv.Itoa(len(resp.Body))
}

func traceRequest(request *http.Request, tr tracer.Tracer, command parser.Command, HoneypotDescription, body string, meta map[string]string, trustedProxies []*net.IPNet) {
	host, port := realClientAddr(request, trustedProxies)

	remoteAddr := host
	if port != "" {
		remoteAddr = net.JoinHostPort(host, port)
	}
	event := tracer.Event{
		Msg:             "HTTP New request",
		RequestURI:      request.RequestURI,
		Protocol:        tracer.HTTP.String(),
		HTTPMethod:      request.Method,
		Body:            body,
		HostHTTPRequest: request.Host,
		UserAgent:       request.UserAgent(),
		Cookies:         mapCookiesToString(request.Cookies()),
		Headers:         mapHeaderToString(request.Header),
		HeadersMap:      request.Header,
		Status:          tracer.Stateless.String(),
		RemoteAddr:      remoteAddr,
		SourceIp:        host,
		SourcePort:      port,
		ID:              uuid.New().String(),
		Description:     HoneypotDescription,
		Handler:         command.Name,
		Metadata:        meta,
	}
	// Capture the TLS details from the request, if provided.
	if request.TLS != nil {
		event.Msg = "HTTPS New Request"
		event.TLSServerName = request.TLS.ServerName
	}
	tr.TraceEvent(event)
}

// realClientAddr returns the host and port of the real client.
//
// The immediate TCP peer (request.RemoteAddr) is the only non-spoofable source
// of identity: it is set by Go's net/http server from the socket peer address.
// X-Forwarded-For and X-Real-IP are application-level headers and trivially
// forgeable when the honeypot is reachable directly from the Internet.
//
// Algorithm:
//   - If trustedProxies is empty OR the immediate peer is not in trustedProxies,
//     headers are ignored entirely and the peer address is returned. This
//     protects deployments where beelzebub is exposed directly: an attacker
//     setting "X-Forwarded-For: 8.8.8.8" cannot make us log 8.8.8.8.
//   - If the peer is a trusted proxy, X-Forwarded-For is parsed right-to-left
//     and the first entry that is NOT itself a trusted hop is treated as the
//     real client. This neutralizes XFF poisoning, where a client sends a
//     pre-filled XFF and the trusted proxy appends the real peer to it
//     ("spoofed, real-peer"): walking from the right skips the trusted hops
//     until the legitimate client IP appears.
//   - If XFF is empty or contains only trusted hops, X-Real-IP is consulted as
//     a single-hop fallback. If it too is missing or trusted, the peer
//     address is returned.
//
// The returned port mirrors the peer's port; XFF/X-Real-IP do not carry port
// information, so when the address comes from a header the port is empty.
func realClientAddr(r *http.Request, trustedProxies []*net.IPNet) (host, port string) {
	host, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
		port = ""
	}
	if len(trustedProxies) == 0 {
		return host, port
	}
	peerIP := net.ParseIP(host)
	if peerIP == nil || !ipInNets(peerIP, trustedProxies) {
		return host, port
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.TrimSpace(parts[i])
			if candidate == "" {
				continue
			}
			ip := net.ParseIP(candidate)
			if ip == nil {
				continue
			}
			if !ipInNets(ip, trustedProxies) {
				return candidate, ""
			}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil && !ipInNets(ip, trustedProxies) {
			return xri, ""
		}
	}
	return host, port
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func mapHeaderToString(headers http.Header) string {
	headersString := ""

	for key := range headers {
		for _, values := range headers[key] {
			headersString += fmt.Sprintf("[Key: %s, values: %s],", key, values)
		}
	}

	return headersString
}

func mapCookiesToString(cookies []*http.Cookie) string {
	cookiesString := ""

	for _, cookie := range cookies {
		cookiesString += cookie.String()
	}

	return cookiesString
}

func setResponseHeaders(responseWriter http.ResponseWriter, headers []string, statusCode int) {
	for _, headerStr := range headers {
		keyValue := strings.Split(headerStr, ":")
		if len(keyValue) > 1 {
			responseWriter.Header().Add(keyValue[0], keyValue[1])
		}
	}
	// http.StatusText(statusCode): empty string if the code is unknown.
	if len(http.StatusText(statusCode)) > 0 {
		responseWriter.WriteHeader(statusCode)
	}
}
