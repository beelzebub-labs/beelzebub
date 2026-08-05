package pluginmgr

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderImports_Empty(t *testing.T) {
	src, err := renderImports(nil)
	require.NoError(t, err)
	assertParses(t, src)
	assert.NotContains(t, string(src), "import (")
}

func TestRenderImports_SortedDeduped(t *testing.T) {
	src, err := renderImports([]string{
		"github.com/acme/zeta",
		"github.com/acme/alpha",
		"github.com/acme/zeta", // duplicate
		"",                     // skipped
	})
	require.NoError(t, err)
	assertParses(t, src)

	s := string(src)
	assert.Equal(t, 1, strings.Count(s, `_ "github.com/acme/zeta"`), "duplicate import must collapse")
	assert.Less(t, strings.Index(s, "alpha"), strings.Index(s, "zeta"), "imports must be sorted")
}

func TestWriteImportsFile(t *testing.T) {
	dir := t.TempDir()
	lf := &LockFile{Plugins: []LockedPlugin{
		{Name: "scanner", Import: "github.com/acme/scanner"},
	}}
	require.NoError(t, writeImportsFile(dir, lf))

	raw, err := os.ReadFile(filepath.Join(dir, generatedFileName))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `_ "github.com/acme/scanner"`)
	assert.Contains(t, string(raw), "package plugins")
	assertParses(t, raw)
}

func TestWriteImportsFile_CreateDirError(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a dir"), 0o644))

	err := writeImportsFile(filepath.Join(blocker, "plugins"), &LockFile{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating plugins dir")
}

func assertParses(t *testing.T, src []byte) {
	t.Helper()
	_, err := parser.ParseFile(token.NewFileSet(), "imports_gen.go", src, parser.AllErrors)
	require.NoError(t, err, "generated file must be valid Go:\n%s", src)
}
