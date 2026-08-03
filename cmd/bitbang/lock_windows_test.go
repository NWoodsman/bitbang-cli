//go:build windows

package main

import (
	"os"
	"testing"
)

func TestIdentityLockWindows(t *testing.T) {
	dir := t.TempDir()

	l1, _, err := acquireIdentityLock(dir)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer l1.release()

	l2, pid, err := acquireIdentityLock(dir)
	if err != errIdentityBusy {
		t.Fatalf("second acquire: err=%v, want errIdentityBusy", err)
	}
	if l2 != nil {
		t.Fatal("second acquire returned a non-nil lock while busy")
	}
	if pid != os.Getpid() {
		t.Errorf("busy PID = %d, want %d", pid, os.Getpid())
	}

	l1.release()
	l1 = nil
	l3, _, err := acquireIdentityLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	l3.release()
}
