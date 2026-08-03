//go:build !unix

package main

import "os"

// Windows consoles do not expose SIGWINCH. The initial dimensions are still
// sent, and the channel remains idle for the lifetime of the session.
func notifyWindowChanges(chan<- os.Signal) {}
