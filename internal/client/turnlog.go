package client

import (
	"io"

	"github.com/pion/logging"
)

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
// problem: a connector with no direct path available sees the same scope
// report that it cannot allocate at all, and that message is the whole
// diagnostic. -v is the switch between "I am using this" and "I am debugging
// this".
//
// Only the turnc scope is affected. Every other pion scope logs as before.
const turnLogScope = "turnc"

type quietTURNLoggerFactory struct {
	inner   logging.LoggerFactory
	verbose bool
}

func newQuietTURNLoggerFactory(verbose bool) logging.LoggerFactory {
	return &quietTURNLoggerFactory{
		inner:   logging.NewDefaultLoggerFactory(),
		verbose: verbose,
	}
}

func (f *quietTURNLoggerFactory) NewLogger(scope string) logging.LeveledLogger {
	if scope == turnLogScope && !f.verbose {
		return logging.NewDefaultLeveledLoggerForScope(scope, logging.LogLevelDisabled, io.Discard)
	}
	// Everything else, including turnc under -v, gets pion's default
	// behavior -- which keeps PION_LOG_* working rather than overriding it.
	return f.inner.NewLogger(scope)
}
