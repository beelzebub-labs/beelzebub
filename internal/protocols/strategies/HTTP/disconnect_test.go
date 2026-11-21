package HTTP

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/testutil/durabletest"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
)

type disconnectSink struct {
	capture tracer.Tracer
	read    func() []tracer.Event
}

type disconnectFactory struct {
	name string
	new  func(*testing.T, int) *disconnectSink
}

var disconnectFactories = []disconnectFactory{{"legacy", disconnectLegacy}, {"durable", func(t *testing.T, n int) *disconnectSink {
	replay := durabletest.New(t)
	return &disconnectSink{capture: replay.Tracer, read: replay.Events}
}}}

func disconnectLegacy(t *testing.T, n int) *disconnectSink {
	events := make(chan tracer.Event, n+64)
	base := tracer.GetInstance(func(e tracer.Event) { events <- e })
	previous := base.GetStrategy()
	base.SetStrategy(func(e tracer.Event) { events <- e })
	t.Cleanup(func() { base.SetStrategy(previous) })

	return &disconnectSink{capture: base, read: func() []tracer.Event {
		var result []tracer.Event
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()

		for {
			select {
			case e := <-events:
				result = append(result, e)
			case <-timer.C:
				return result
			}
		}
	}}
}

func disconnectConfig() parser.BeelzebubServiceConfiguration {
	return parser.BeelzebubServiceConfiguration{Protocol: "http", Description: "synthetic review", Commands: []parser.Command{{Regex: regexp.MustCompile(".*"), Handler: "synthetic", StatusCode: 200}}}
}

func TestCompletedHTTPEventAdmitsCanceledContext(t *testing.T) {
	for _, factory := range disconnectFactories {
		t.Run(factory.name, func(t *testing.T) {
			sink := factory.new(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			request := httptest.NewRequest("POST", "http://synthetic.test/login", bytes.NewBufferString("user=admin&pass=123456")).WithContext(ctx)
			newHTTPHandler(disconnectConfig(), sink.capture)(httptest.NewRecorder(), request)
			events := sink.read()
			t.Logf("DISCONNECT already_canceled mode=%s captured=%d expected=1", factory.name, len(events))

			if len(events) != 1 {
				t.Errorf("complete received request discarded although capture has capacity")
				return
			}

			if events[0].Body != "user=admin&pass=123456" {
				t.Errorf("request body changed: %q", events[0].Body)
			}
		})
	}
}

func TestCompletedHTTPEventSurvivesRealDisconnect(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		for _, factory := range disconnectFactories {
			t.Run(fmt.Sprintf("%s/tls=%v", factory.name, useTLS), func(t *testing.T) {
				sink := factory.new(t, 1)
				done := make(chan error, 1)
				handler := newHTTPHandler(disconnectConfig(), sink.capture)
				bodyReady := make(chan struct{})

				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						done <- err
						return
					}

					close(bodyReady)
					// Schedule the same real TCP disconnect before event capture. The
					// full body is verified before waiting for net/http cancellation.
					select {
					case <-r.Context().Done():
					case <-time.After(3 * time.Second):
						done <- fmt.Errorf("disconnect did not cancel HTTP context")
						return
					}

					if string(body) != "user=admin&pass=123456" {
						done <- fmt.Errorf("body incomplete: %q", body)
						return
					}

					r.Body = io.NopCloser(bytes.NewReader(body))
					handler(w, r)
					done <- nil
				}))

				if useTLS {
					server.StartTLS()
				} else {
					server.Start()
				}

				defer server.Close()
				parsed, _ := url.Parse(server.URL)
				conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
				if err != nil {
					t.Fatal(err)
				}

				raw := conn
				if useTLS {
					secure := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
					if err := secure.Handshake(); err != nil {
						raw.Close()
						t.Fatal(err)
					}

					conn = secure
				}

				wire := "POST /login HTTP/1.1\r\nHost: synthetic.test\r\nContent-Length: 22\r\n\r\nuser=admin&pass=123456"
				_, err = io.WriteString(conn, wire)
				select {
				case <-bodyReady:
				case <-time.After(3 * time.Second):
					raw.Close()
					t.Fatal("body was not fully received")
				}

				raw.Close()
				if err != nil {
					t.Fatal(err)
				}

				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request handler did not finish")
				}

				events := sink.read()
				t.Logf("DISCONNECT verified_full_body_disconnect mode=%s captured=%d expected=1", factory.name, len(events))
				if len(events) != 1 {
					t.Errorf("real disconnect discarded a verified complete body")
				}
			})
		}
	}
}
