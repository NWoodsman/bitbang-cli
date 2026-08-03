//go:build windows

package streamtype

import (
	"os"
	"reflect"
	"testing"
)

func TestDefaultShellArgvWindows(t *testing.T) {
	t.Setenv("COMSPEC", `C:\Windows\System32\cmd.exe`)
	want := []string{`C:\Windows\System32\cmd.exe`}
	if got := defaultShellArgv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defaultShellArgv() = %q, want %q", got, want)
	}
}

func TestDefaultShellArgvWindowsFallback(t *testing.T) {
	t.Setenv("COMSPEC", "")
	if got := defaultShellArgv(); !reflect.DeepEqual(got, []string{"cmd.exe"}) {
		t.Fatalf("defaultShellArgv() = %q, want [cmd.exe]", got)
	}
}

func TestWindowsSignalMapping(t *testing.T) {
	if got := signalFromName("INT"); got != os.Kill {
		t.Errorf("INT maps to %v, want os.Kill", got)
	}
	if got := signalFromName("KILL"); got != os.Kill {
		t.Errorf("KILL maps to %v, want os.Kill", got)
	}
	if got := signalFromName("USR1"); got != nil {
		t.Errorf("USR1 maps to %v, want nil on Windows", got)
	}
}

func TestWindowsPTYEnabled(t *testing.T) {
	if !platformSupportsPTY {
		t.Fatal("Windows should advertise ConPTY support")
	}
}
