package HTTP

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponseHeaderValuesPreserveColonsOnWire(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setResponseHeaders(w, []string{
			"Location: http://example.invalid/a:b",
			"Last-Modified: Thu, 01 Jan 1970 00:00:01 GMT",
			"Set-Cookie: a=one:two; Path=/",
			"Set-Cookie: b=three:four; Path=/",
			"X-Simple: unchanged",
		}, http.StatusOK)
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	for name, want := range map[string]string{
		"Location":      "http://example.invalid/a:b",
		"Last-Modified": "Thu, 01 Jan 1970 00:00:01 GMT",
		"X-Simple":      "unchanged",
	} {
		if got := response.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	cookies := response.Header.Values("Set-Cookie")
	if len(cookies) != 2 || cookies[0] != "a=one:two; Path=/" || cookies[1] != "b=three:four; Path=/" {
		t.Errorf("Set-Cookie values = %q", cookies)
	}
}
