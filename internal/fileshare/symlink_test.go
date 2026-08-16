package fileshare

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// share builds a browse-mode share rooted at dir.
func share(dir string, upload bool) *FileShare {
	return &FileShare{BasePath: dir, Mode: ModeBrowse, UploadEnabled: upload}
}

// failOnLeakedHandles reports any file under dir still open when the test
// ends.
//
// This exists because the failure is otherwise invisible on Linux and
// baffling on Windows. A test that forgets to close an io.ReadCloser leaves
// the file open; Linux unlinks it happily, so t.TempDir cleans up and the
// test passes. Windows refuses to delete an open file, so cleanup fails and
// the error surfaces against testing.go rather than against the line that
// opened it -- on the one platform that is hardest to reproduce locally.
//
// Register it *after* t.TempDir. Cleanups run last-registered-first, so this
// runs while the directory still exists.
//
// Linux-only, since it reads /proc/self/fd. That is enough: the point is to
// fail on the platform where the problem is easy to see.
func failOnLeakedHandles(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		if runtime.GOOS != "linux" {
			return
		}
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return
		}
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			return
		}
		var leaked []string
		for _, e := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
			if err != nil {
				continue // fd closed while we walked, or not a symlink
			}
			if strings.HasPrefix(target, root+string(os.PathSeparator)) {
				leaked = append(leaked, target)
			}
		}
		for _, path := range leaked {
			t.Errorf("file left open at test end: %s\n"+
				"\tclose every io.ReadCloser/io.WriteCloser the test obtains, or "+
				"t.TempDir cleanup fails on Windows", path)
		}
	})
}

// outside creates a sibling directory holding a secret file, plus the share
// root itself. Returned as (shareDir, outsideDir).
func outside(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	failOnLeakedHandles(t, root)
	shareDir := filepath.Join(root, "share")
	outsideDir := filepath.Join(root, "outside")
	for _, d := range []string{shareDir, outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	return shareDir, outsideDir
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require elevation on Windows")
		}
		t.Fatal(err)
	}
}

func TestSafePathRejectsSymlinkedFile(t *testing.T) {
	shareDir, outsideDir := outside(t)
	symlink(t, filepath.Join(outsideDir, "secret.txt"), filepath.Join(shareDir, "link.txt"))

	if got := SafePath(shareDir, "link.txt"); got != "" {
		t.Errorf("SafePath followed a symlink out of the share: %q", got)
	}
}

func TestSafePathRejectsSymlinkedDir(t *testing.T) {
	shareDir, outsideDir := outside(t)
	symlink(t, outsideDir, filepath.Join(shareDir, "link"))

	if got := SafePath(shareDir, "link/secret.txt"); got != "" {
		t.Errorf("SafePath read through a directory symlink: %q", got)
	}
	if got := SafePath(shareDir, "link"); got != "" {
		t.Errorf("SafePath accepted a directory symlink pointing outside: %q", got)
	}
}

// The share root being a symlink is legitimate and common -- macOS /tmp is a
// symlink to /private/tmp. Resolving the requested path but not the base
// makes every such share reject everything, which is the standard way this
// fix ships broken.
func TestSafePathAllowsSymlinkedBase(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "ok.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	symlink(t, real, link)

	got := SafePath(link, "ok.txt")
	if got == "" {
		t.Fatal("SafePath rejected a file in a share whose root is a symlink")
	}
	if !strings.HasSuffix(got, "ok.txt") {
		t.Errorf("unexpected path %q", got)
	}
	if SafePath(link, "") == "" {
		t.Error("SafePath rejected the share root itself when it is a symlink")
	}
}

func TestSafePathStillRejectsTraversal(t *testing.T) {
	shareDir, _ := outside(t)
	for _, p := range []string{"../outside/secret.txt", "..", "a/../../outside/secret.txt"} {
		if got := SafePath(shareDir, p); got != "" {
			t.Errorf("SafePath(%q) = %q, want rejection", p, got)
		}
	}
}

func TestSafePathAllowsOrdinaryFile(t *testing.T) {
	shareDir, _ := outside(t)
	if err := os.WriteFile(filepath.Join(shareDir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if SafePath(shareDir, "a.txt") == "" {
		t.Error("SafePath rejected an ordinary file inside the share")
	}
}

func TestOpenWriteRejectsSymlinkedDir(t *testing.T) {
	shareDir, outsideDir := outside(t)
	symlink(t, outsideDir, filepath.Join(shareDir, "link"))

	f := share(shareDir, true)
	w, err := f.OpenWrite("link/planted.txt", true)
	if err == nil {
		w.Close()
		t.Fatal("OpenWrite created a file through a directory symlink")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, "planted.txt")); statErr == nil {
		t.Fatal("a file was written outside the share")
	}
}

// An existing symlink under the share is the other write escape: the parent
// is legitimately inside, but os.Create follows the final component.
func TestOpenWriteRejectsExistingSymlinkTarget(t *testing.T) {
	shareDir, outsideDir := outside(t)
	victim := filepath.Join(outsideDir, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, victim, filepath.Join(shareDir, "innocent.txt"))

	f := share(shareDir, true)
	w, err := f.OpenWrite("innocent.txt", true)
	if err == nil {
		w.Close()
		t.Fatal("OpenWrite followed a symlink out of the share")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("file outside the share was overwritten: %q", got)
	}
}

func TestOpenWriteAllowsOrdinaryNewFile(t *testing.T) {
	shareDir, _ := outside(t)
	f := share(shareDir, true)
	w, err := f.OpenWrite("new.txt", true)
	if err != nil {
		t.Fatalf("OpenWrite rejected an ordinary new file: %v", err)
	}
	if _, err := io.WriteString(w, "data"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := os.Stat(filepath.Join(shareDir, "new.txt")); err != nil {
		t.Fatalf("file was not created: %v", err)
	}
}

// Hiding an entry from the listing is not a read control unless the read
// paths enforce it too.
func TestReadPathsEnforceShouldShow(t *testing.T) {
	shareDir, _ := outside(t)
	if err := os.WriteFile(filepath.Join(shareDir, ".env"), []byte("SECRET=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(shareDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := share(shareDir, false)
	for _, p := range []string{".env", ".git/config", ".git"} {
		// Close on the unexpected-success path too: t.TempDir cleanup fails
		// on Windows if anything still holds the file open, which turns a
		// leaked handle into a confusing failure in an unrelated place.
		if r, _, err := f.OpenRead(p); err == nil {
			r.Close()
			t.Errorf("OpenRead(%q) succeeded; hidden entries must not be readable by path", p)
		}
		if _, err := f.StatPath(p); err == nil {
			t.Errorf("StatPath(%q) succeeded; hidden entries must not be stat-able by path", p)
		}
	}

	// A normal file in the same share is unaffected.
	if err := os.WriteFile(filepath.Join(shareDir, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _, err := f.OpenRead("visible.txt")
	if err != nil {
		t.Fatalf("OpenRead rejected an ordinary file: %v", err)
	}
	r.Close()
}
