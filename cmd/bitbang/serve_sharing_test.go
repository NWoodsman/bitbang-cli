package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richlegrand/bitbang/internal/fileshare"
)

// The Sharing block is the listener's answer to "what did I just expose",
// printed once at startup and never asserted anywhere until now. Pin the
// exact wording across the mode matrix, so a refactor that reorganizes how
// capabilities are described cannot quietly reword or drop a line.
func TestSharingBlock(t *testing.T) {
	dir := t.TempDir()
	single := filepath.Join(dir, "one.bin")
	if err := os.WriteFile(single, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	shareDir, err := fileshare.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	shareFile, err := fileshare.New(single)
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := fileshare.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	uploads.UploadEnabled = true

	cases := []struct {
		name  string
		cfg   serveConfig
		share *fileshare.FileShare
		want  []string
	}{
		{
			name: "shell only, defaults",
			cfg:  serveConfig{shellEnabled: true, shellMaxSessions: 1},
			want: []string{
				"Sharing:",
				"  • shell  (" + defaultShellLabel() + ")",
				"  • tcp    (unrestricted targets chosen by connect -L; max 64 concurrent connections per session; loopback-bound on connector by default)",
				"",
			},
		},
		{
			name: "shell with a command, session cap, and mirroring",
			cfg: serveConfig{
				shellEnabled: true, shellCmd: "/bin/zsh",
				shellMaxSessions: 3, shellMirror: true,
			},
			want: []string{
				"Sharing:",
				"  • shell  (/bin/zsh, max 3 concurrent sessions, mirroring to console)",
				"  • tcp    (unrestricted targets chosen by connect -L; max 64 concurrent connections per session; loopback-bound on connector by default)",
				"",
			},
		},
		{
			name: "unlimited shell sessions",
			cfg:  serveConfig{shellEnabled: true, shellMaxSessions: 0},
			want: []string{
				"Sharing:",
				"  • shell  (" + defaultShellLabel() + ", unlimited concurrent sessions)",
				"  • tcp    (unrestricted targets chosen by connect -L; max 64 concurrent connections per session; loopback-bound on connector by default)",
				"",
			},
		},
		{
			name:  "files, a directory",
			cfg:   serveConfig{filesEnabled: true},
			share: shareDir,
			want:  []string{"Sharing:", "  • files  (" + dir + ")", ""},
		},
		{
			name:  "files, a directory with uploads",
			cfg:   serveConfig{filesEnabled: true},
			share: uploads,
			want:  []string{"Sharing:", "  • files  (" + dir + ", uploads enabled)", ""},
		},
		{
			name:  "files, a single file",
			cfg:   serveConfig{filesEnabled: true},
			share: shareFile,
			want:  []string{"Sharing:", "  • files  (one.bin — single file)", ""},
		},
		{
			name: "proxy, target chosen in the browser",
			cfg:  serveConfig{proxyEnabled: true},
			want: []string{"Sharing:", "  • proxy  (target chosen in browser)", ""},
		},
		{
			name: "proxy, fixed target",
			cfg:  serveConfig{proxyEnabled: true, target: "localhost:5000"},
			want: []string{"Sharing:", "  • proxy  (localhost:5000)", ""},
		},
		{
			name: "all caps, in order",
			cfg: serveConfig{
				shellEnabled: true, filesEnabled: true, proxyEnabled: true,
				shellMaxSessions: 1,
			},
			share: shareDir,
			want: []string{
				"Sharing:",
				"  • shell  (" + defaultShellLabel() + ")",
				"  • tcp    (unrestricted targets chosen by connect -L; max 64 concurrent connections per session; loopback-bound on connector by default)",
				"  • files  (" + dir + ")",
				"  • proxy  (target chosen in browser)",
				"",
			},
		},
		{
			name: "files enabled but no share is silent",
			cfg:  serveConfig{filesEnabled: true},
			want: []string{"Sharing:", ""},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			printSharingBlock(&buf, c.cfg, c.share)
			got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
			if len(got) != len(c.want) {
				t.Fatalf("got %d lines, want %d:\n%q", len(got), len(c.want), buf.String())
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("line %d:\n got %q\nwant %q", i+1, got[i], c.want[i])
				}
			}
		})
	}
}
