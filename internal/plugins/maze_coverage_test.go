package plugins

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMazeHoneypot_FileResponse_ContentTypes(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		contentTy string
	}{
		{name: "html extension", path: "/index.htm", contentTy: "text/html; charset=UTF-8"},
		{name: "yaml extension", path: "/config/settings.yml", contentTy: "text/yaml; charset=UTF-8"},
		{name: "csv extension", path: "/data/users.csv", contentTy: "text/csv; charset=UTF-8"},
		{name: "python extension", path: "/src/service.py", contentTy: "text/x-python"},
		{name: "go extension", path: "/src/service.go", contentTy: "text/x-go"},
		{name: "shell extension", path: "/bin/service.sh", contentTy: "application/x-sh"},
		{name: "markdown extension", path: "/docs/changelog.md", contentTy: "text/markdown; charset=UTF-8"},
		{name: "xml extension", path: "/config/service.xml", contentTy: "application/xml"},
	}

	maze := &MazeHoneypot{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := maze.generateFileResponse(tt.path)
			assert.Equal(t, tt.contentTy, response.ContentType)
			assert.NotEmpty(t, response.Body)
		})
	}
}

func TestMazeHoneypot_FileAndDotfileDispatch(t *testing.T) {
	maze := &MazeHoneypot{}
	for _, path := range []string{".", "/.bashrc", "/.htaccess", "/.gitignore", "/Makefile", "/Dockerfile", "/misc/unknown.ext"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			if path == "." {
				request.URL = &url.URL{Path: "."}
			} else {
				request.URL.Path = path
			}
			response := maze.HandleRequest(request)
			if response.StatusCode != http.StatusOK || response.Body == "" {
				t.Fatalf("HandleRequest(%q) = status %d, empty body", path, response.StatusCode)
			}
		})
	}
}

func TestMazeHoneypot_DockerfileVariants(t *testing.T) {
	for i := 0; i < 8; i++ {
		body := genDockerfile(rand.New(rand.NewSource(int64(i))), "/Dockerfile")
		if body == "" {
			t.Fatal("Dockerfile response is empty")
		}
	}
}

func TestMazeHoneypot_FallbackGenerators(t *testing.T) {
	maze := &MazeHoneypot{}
	oldTemplates := allFileTemplates
	allFileTemplates = nil
	t.Cleanup(func() { allFileTemplates = oldTemplates })
	paths := []string{
		"/.htaccess", "/.gitignore", "/Makefile", "/Dockerfile",
		"/fallback.sql", "/fallback.log", "/fallback.php", "/fallback.py",
		"/fallback.go", "/fallback.sh", "/fallback.yaml", "/fallback.json",
		"/fallback.conf", "/fallback.csv", "/fallback.md", "/fallback.txt",
		"/fallback.tar.gz", "/fallback.zip", "/fallback.key", "/fallback.pem",
		"/fallback.unknown",
	}
	if !isFilePath(".bashrc") {
		t.Fatal("dotfile without extension should be treated as a file")
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := maze.generateFileResponse(path)
			if response.Body == "" {
				t.Fatalf("generateFileResponse(%q) returned empty body", path)
			}
		})
	}
	for i := 0; i < 32; i++ {
		if body := genSQLDump(rand.New(rand.NewSource(int64(i))), "/fallback.sql"); body == "" {
			t.Fatal("SQL dump generator returned empty body")
		}
	}
}
