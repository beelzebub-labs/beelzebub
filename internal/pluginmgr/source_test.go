package pluginmgr

import "testing"

func TestParseSource(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantURL  string
		wantRepo string
		wantRef  string
		wantSSH  bool
		wantErr  bool
	}{
		{
			name:     "bare host/owner/repo",
			in:       "github.com/beelzebub-labs/scanner",
			wantURL:  "https://github.com/beelzebub-labs/scanner.git",
			wantRepo: "scanner",
		},
		{
			name:     "https with .git",
			in:       "https://github.com/beelzebub-labs/scanner.git",
			wantURL:  "https://github.com/beelzebub-labs/scanner.git",
			wantRepo: "scanner",
		},
		{
			name:     "https with trailing ref",
			in:       "https://github.com/beelzebub-labs/scanner@v1.2.3",
			wantURL:  "https://github.com/beelzebub-labs/scanner.git",
			wantRepo: "scanner",
			wantRef:  "v1.2.3",
		},
		{
			name:     "bare with ref",
			in:       "github.com/acme/foo@main",
			wantURL:  "https://github.com/acme/foo.git",
			wantRepo: "foo",
			wantRef:  "main",
		},
		{
			name:     "scp-style ssh",
			in:       "git@github.com:beelzebub-labs/scanner.git",
			wantURL:  "git@github.com:beelzebub-labs/scanner.git",
			wantRepo: "scanner",
			wantSSH:  true,
		},
		{
			name:     "scp-style ssh with ref",
			in:       "git@github.com:acme/foo.git@v2.0.0",
			wantURL:  "git@github.com:acme/foo.git",
			wantRepo: "foo",
			wantRef:  "v2.0.0",
			wantSSH:  true,
		},
		{
			name:     "ssh:// url",
			in:       "ssh://git@github.com/beelzebub-labs/scanner.git",
			wantURL:  "ssh://git@github.com/beelzebub-labs/scanner.git",
			wantRepo: "scanner",
			wantSSH:  true,
		},
		{name: "empty", in: "", wantErr: true},
		{name: "no repo", in: "github.com/owner", wantErr: true},
		{name: "ssh missing colon", in: "git@github.com/owner/repo", wantErr: true},
		{name: "local path does not exist", in: "/no/such/plugin/dir", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSource(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSource(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSource(%q) unexpected error: %v", tt.in, err)
			}
			if got.CloneURL != tt.wantURL {
				t.Errorf("CloneURL = %q, want %q", got.CloneURL, tt.wantURL)
			}
			if got.Repo != tt.wantRepo {
				t.Errorf("Repo = %q, want %q", got.Repo, tt.wantRepo)
			}
			if got.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", got.Ref, tt.wantRef)
			}
			if got.SSH != tt.wantSSH {
				t.Errorf("SSH = %v, want %v", got.SSH, tt.wantSSH)
			}
		})
	}
}

func TestParseSource_Local(t *testing.T) {
	dir := t.TempDir()
	got, err := ParseSource(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Local {
		t.Errorf("Local = false, want true")
	}
	if got.CloneURL != "file://"+dir {
		t.Errorf("CloneURL = %q, want %q", got.CloneURL, "file://"+dir)
	}
	if got.Repo != filepathBase(dir) {
		t.Errorf("Repo = %q, want %q", got.Repo, filepathBase(dir))
	}
}

func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
