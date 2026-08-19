package turnlog

import (
	"os"
	"testing"

	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
)

func TestDisableTURNScope(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		want     string
	}{
		{"unset", "", "turnc"},
		{"keeps other scopes", "ice,dtls", "ice,dtls,turnc"},
		{"already present", "turnc", "turnc"},
		{"already present among others", "ice,turnc,dtls", "ice,turnc,dtls"},
		{"case-insensitive match", "ICE,TURNC", "ICE,TURNC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(disableEnv, tt.existing)
			if tt.existing == "" {
				os.Unsetenv(disableEnv)
			}
			disableTURNScope()
			if got := os.Getenv(disableEnv); got != tt.want {
				t.Errorf("%s = %q, want %q", disableEnv, got, tt.want)
			}
		})
	}
}

// The env var is what actually reaches the TURN client: pion/webrtc builds its
// ICE agent from options, and ice's WithLoggerFactory doesn't set the agent
// field that gather.go hands to turn.ClientConfig. See the package comment.
// This asserts the env name and value still mean what we think to pion.
func TestDisableEnvSilencesTURNInPionsOwnFactory(t *testing.T) {
	t.Setenv(disableEnv, turnLogScope)
	f := logging.NewDefaultLoggerFactory()
	if lvl, ok := f.ScopeLevels[turnLogScope]; !ok || lvl != logging.LogLevelDisabled {
		t.Fatalf("turnc scope level = %v (present=%v), want disabled", lvl, ok)
	}
	if _, ok := f.ScopeLevels["ice"]; ok {
		t.Error("ice scope should be untouched")
	}
}

func TestInstallSetsLoggerFactory(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		var se webrtc.SettingEngine
		Install(&se, verbose)
		if se.LoggerFactory == nil {
			t.Errorf("verbose=%v: LoggerFactory not set", verbose)
		}
	}
}

func TestFactoryDisablesTURNScopeOnlyWhenQuiet(t *testing.T) {
	quiet := newFactory(false)
	if lg, ok := quiet.NewLogger(turnLogScope).(*logging.DefaultLeveledLogger); !ok || lg == nil {
		t.Fatal("turnc logger missing")
	}
	// A disabled logger drops writes; nothing to assert but that it does not
	// panic and does not reach stderr.
	quiet.NewLogger(turnLogScope).Error("must not appear")

	if quiet.NewLogger("ice") == nil {
		t.Error("ice logger should come from the inner factory")
	}
	if newFactory(true).NewLogger(turnLogScope) == nil {
		t.Error("verbose turnc logger should come from the inner factory")
	}
}
