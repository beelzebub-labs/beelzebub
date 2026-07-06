package pluginmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type RepoSource struct {
	Raw      string
	CloneURL string
	Host     string
	Owner    string
	Repo     string
	Ref      string
	SSH      bool
	Local    bool
}

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ParseSource(raw string) (RepoSource, error) {
	s := RepoSource{Raw: strings.TrimSpace(raw)}
	in := s.Raw
	if in == "" {
		return s, fmt.Errorf("empty plugin source")
	}

	if isLocalPath(in) {
		return parseLocal(s, in)
	}

	switch {
	case strings.HasPrefix(in, "git@"):
		s.SSH = true
		body, ref := splitTrailingRef(in)
		s.Ref = ref
		at := strings.Index(body, "@")
		colon := strings.Index(body, ":")
		if colon < 0 || colon < at {
			return s, fmt.Errorf("invalid ssh source %q", raw)
		}
		s.Host = body[at+1 : colon]
		if err := s.setOwnerRepo(body[colon+1:]); err != nil {
			return s, err
		}
		s.CloneURL = fmt.Sprintf("git@%s:%s/%s.git", s.Host, s.Owner, s.Repo)

	case strings.HasPrefix(in, "ssh://"):
		s.SSH = true
		body, ref := splitTrailingRef(strings.TrimPrefix(in, "ssh://"))
		s.Ref = ref
		host, path := splitHostPath(body)
		if err := s.setOwnerRepo(path); err != nil {
			return s, err
		}
		s.Host = strings.TrimPrefix(host, "git@")
		s.CloneURL = fmt.Sprintf("ssh://%s/%s/%s.git", host, s.Owner, s.Repo)

	default:
		u := strings.TrimPrefix(strings.TrimPrefix(in, "https://"), "http://")
		body, ref := splitTrailingRef(u)
		s.Ref = ref
		host, path := splitHostPath(body)
		if host == "" || path == "" {
			return s, fmt.Errorf("invalid source %q (expected host/owner/repo)", raw)
		}
		s.Host = host
		if err := s.setOwnerRepo(path); err != nil {
			return s, err
		}
		s.CloneURL = fmt.Sprintf("https://%s/%s/%s.git", s.Host, s.Owner, s.Repo)
	}

	return s, nil
}

func isLocalPath(raw string) bool {
	return strings.HasPrefix(raw, "file://") ||
		strings.HasPrefix(raw, "/") ||
		strings.HasPrefix(raw, "./") ||
		strings.HasPrefix(raw, "../") ||
		strings.HasPrefix(raw, "~/")
}

func parseLocal(s RepoSource, in string) (RepoSource, error) {
	s.Local = true
	body := strings.TrimPrefix(in, "file://")
	if strings.HasPrefix(body, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return s, fmt.Errorf("resolving ~: %w", err)
		}
		body = filepath.Join(home, body[2:])
	}
	abs, err := filepath.Abs(body)
	if err != nil {
		return s, fmt.Errorf("resolving local path %q: %w", in, err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return s, fmt.Errorf("local plugin path %q is not a directory", abs)
	}
	s.Repo = filepath.Base(abs)
	s.CloneURL = "file://" + abs
	return s, nil
}

func (s *RepoSource) setOwnerRepo(path string) error {
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return fmt.Errorf("cannot determine owner/repo from %q", s.Raw)
	}
	s.Owner = parts[0]
	s.Repo = parts[len(parts)-1]
	if !repoNameRe.MatchString(s.Repo) {
		return fmt.Errorf("invalid repo name %q in %q", s.Repo, s.Raw)
	}
	return nil
}

func splitTrailingRef(str string) (body, ref string) {
	start := 0
	seg := str
	if slash := strings.LastIndex(str, "/"); slash >= 0 {
		seg = str[slash+1:]
		start = slash + 1
	}
	if at := strings.LastIndex(seg, "@"); at >= 0 {
		return str[:start+at], seg[at+1:]
	}
	return str, ""
}

func splitHostPath(s string) (host, path string) {
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
