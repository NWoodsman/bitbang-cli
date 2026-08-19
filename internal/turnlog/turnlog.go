// Package turnlog suppresses pion's turnc scope unless -v is given.
//
// A TURN allocation is made whenever ICE gathers a relay candidate, and pion
// keeps it alive for the agent's lifetime whether or not the relay pair was
// selected. On a direct connection nothing uses it, but the keepalive still
// runs -- and once the credentials expire it fails on every attempt:
//
//	turnc ERROR: Fail to refresh permissions: CreatePermission error response (error 401: Unauthorized)
//	turnc ERROR: Fail to refresh permissions: write tcp4 ...:5349: write: connection reset by peer
//
// Two lines every two minutes, forever, on a connection that is working
// perfectly. Harmless, and unreadable if you are trying to use the terminal --
// which `connect -L` now means, since a forward runs unattended for hours
// where an interactive shell did not.
//
// These are dropped by default and restored by -v, which puts the scope back
// on pion's default behavior rather than inventing a level for it. Dropping them
// unconditionally would hide the case where TURN failure is the actual
// problem: a peer with no direct path available sees the same scope report
// that it cannot allocate at all, and that message is the whole diagnostic.
// -v is the switch between "I am using this" and "I am debugging this".
//
// Both peers create their own PeerConnection -- internal/client for the
// connector, internal/peer for the listener -- and both need this. The
// listener is the one that runs for hours, so leaving it out defeats the
// purpose.
//
// # Why this takes two mechanisms
//
// SettingEngine.LoggerFactory is the documented way to control pion's logging,
// and it does not reach the TURN client. In pion/ice v4 (checked through
// v4.4.1, the current release) the Agent has two constructors:
//
//   - NewAgent(config) stores config.LoggerFactory on the agent
//   - NewAgentWithOptions(opts...) starts from an empty AgentConfig, so the
//     agent's factory is pion's own default, and WithLoggerFactory only
//     overwrites a.log -- it never assigns a.loggerFactory
//
// pion/webrtc builds its agent from options (ICEGatherer.baseAgentOptions),
// so a.loggerFactory is the default one. gather.go then passes it to
// turn.ClientConfig, and turn/client.go builds the "turnc" logger from that.
// The upshot is that our factory sees the "ice" scope -- WithLoggerFactory
// calls it directly -- and never sees "turnc".
//
// PION_LOG_DISABLE is the way in. Every DefaultLoggerFactory reads it at
// construction, including the one pion builds for us, and a named scope there
// lands in ScopeLevels, which NewLogger checks ahead of the default level.
// It has to be set before the first PeerConnection, which Install guarantees
// by doing both jobs at once.
//
// The factory is still installed, so a fix upstream turns into the mechanism
// we wanted rather than a behavior change here.
package turnlog

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
)

const (
	turnLogScope = "turnc"
	disableEnv   = "PION_LOG_DISABLE"
)

// Install configures se so the turnc scope is silent unless verbose. Call it
// on every SettingEngine before the PeerConnection is created.
func Install(se *webrtc.SettingEngine, verbose bool) {
	se.LoggerFactory = newFactory(verbose)
	if verbose {
		return
	}
	envOnce.Do(disableTURNScope)
}

var envOnce sync.Once

// disableTURNScope adds turnc to PION_LOG_DISABLE, keeping any scopes the
// user named there. Setting the variable is process-wide and pion reads it
// per factory, so it only needs to happen once and only before the first
// agent is built.
func disableTURNScope() {
	existing := os.Getenv(disableEnv)
	if existing == "" {
		_ = os.Setenv(disableEnv, turnLogScope)

		return
	}
	for _, scope := range strings.Split(strings.ToLower(existing), ",") {
		if strings.TrimSpace(scope) == turnLogScope {
			return
		}
	}
	_ = os.Setenv(disableEnv, existing+","+turnLogScope)
}

type quietFactory struct {
	inner   logging.LoggerFactory
	verbose bool
}

func newFactory(verbose bool) logging.LoggerFactory {
	return &quietFactory{
		inner:   logging.NewDefaultLoggerFactory(),
		verbose: verbose,
	}
}

func (f *quietFactory) NewLogger(scope string) logging.LeveledLogger {
	if scope == turnLogScope && !f.verbose {
		return logging.NewDefaultLeveledLoggerForScope(scope, logging.LogLevelDisabled, io.Discard)
	}
	// Everything else, including turnc under -v, gets pion's default
	// behavior -- which keeps PION_LOG_* working rather than overriding it.
	return f.inner.NewLogger(scope)
}
