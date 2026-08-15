package fileshare

import (
	"os"
	"path/filepath"
	"testing"
)

// resolvedTempDir is t.TempDir() with symlinks already resolved.
//
// SafePath returns a fully-resolved path, so a test comparing against a raw
// t.TempDir() passes only where the temp directory happens to contain no
// symlinks. That is true on most Linux boxes and false elsewhere: macOS puts
// temp dirs under /var, a symlink to /private/var, and Windows hands back an
// 8.3 short name that resolution expands. Resolving here keeps the comparison
// about SafePath rather than about the host's temp layout.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return real
}

func TestSafePathInsideBase(t *testing.T) {
	base := resolvedTempDir(t)
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := SafePath(base, "sub")
	if got != sub {
		t.Errorf("SafePath(base, 'sub') = %q, want %q", got, sub)
	}
}

func TestSafePathBaseItself(t *testing.T) {
	base := resolvedTempDir(t)
	got := SafePath(base, "")
	if got != base {
		t.Errorf("SafePath(base, '') = %q, want %q", got, base)
	}
	got = SafePath(base, ".")
	if got != base {
		t.Errorf("SafePath(base, '.') = %q, want %q", got, base)
	}
}

func TestSafePathTraversalRejected(t *testing.T) {
	base := t.TempDir()
	// Make a sibling directory the attacker would want to read.
	parent := filepath.Dir(base)
	sibling := filepath.Join(parent, "sibling")
	_ = os.Mkdir(sibling, 0o755)
	defer os.RemoveAll(sibling)

	cases := []string{
		"../sibling",
		"..",
		"../..",
		"sub/../../sibling",
	}
	for _, in := range cases {
		got := SafePath(base, in)
		if got != "" {
			t.Errorf("SafePath(base, %q) = %q, want empty (traversal)", in, got)
		}
	}
}

func TestSafePathNonexistent(t *testing.T) {
	base := t.TempDir()
	got := SafePath(base, "does-not-exist")
	if got != "" {
		t.Errorf("SafePath of nonexistent path = %q, want empty", got)
	}
}

func TestShouldShow(t *testing.T) {
	cases := []struct {
		name       string
		showHidden bool
		want       bool
	}{
		{"foo.txt", false, true},
		{"foo.txt", true, true},
		{".env", false, false},  // SYSTEM_FILES always hidden
		{".env", true, false},   // even when showHidden
		{".git", false, false},
		{".DS_Store", false, false},
		{"__pycache__", false, false},
		{".hidden", false, false},
		{".hidden", true, true},
	}
	for _, c := range cases {
		got := ShouldShow(c.name, c.showHidden)
		if got != c.want {
			t.Errorf("ShouldShow(%q, %v) = %v, want %v", c.name, c.showHidden, got, c.want)
		}
	}
}
