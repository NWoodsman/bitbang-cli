//go:build !unix

package main

import "os"

// Windows has no SIGHUP. Reload there is the Enter key, and a listener
// running without a console has to be restarted to pick up an edit.
func notifyReload(chan<- os.Signal) {}
