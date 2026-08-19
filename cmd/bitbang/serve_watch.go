package main

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
)

// watchReload runs the two triggers that replace the link table: Enter
// at the console and SIGHUP. Both call reload, which reprints the
// listing and then polls the live sessions, so a deletion takes effect
// at once rather than waiting for the next tick.
//
// The Enter reader only starts on a TTY. Under systemd, nohup, or
// `docker run` without -i, stdin returns EOF immediately and a read loop
// would spin; there, SIGHUP is the only trigger.
func watchReload(reload func()) {
	sighup := make(chan os.Signal, 1)
	notifyReload(sighup)
	go func() {
		for range sighup {
			reload()
		}
	}()

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	go func() {
		// Line mode, not raw: Ctrl-C keeps behaving, and Ctrl-D ends the
		// reader without taking the listener down with it.
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			reload()
		}
	}()
}

// watchExpiry re-checks live sessions on a timer. Deletion is applied
// when the table is replaced; this covers expiry, where the clock moves
// with nobody touching the file.
func watchExpiry(every time.Duration, poll func(time.Time)) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for now := range t.C {
			poll(now)
		}
	}()
}

// reloadHint is the footer under the link listing. Printed only when
// there is a table to reload and someone at a terminal to do it.
func reloadHint() string {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	return fmt.Sprintf("  %-14s %s\n", "Enter:", "reload links.json")
}
