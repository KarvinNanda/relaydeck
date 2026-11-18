package main

import (
	"bytes"
	"os"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestHookAlwaysExitsZero(t *testing.T) {
	t.Setenv("RELAYDECK_URL", "")
	var errb bytes.Buffer
	if code := runHook([]string{"Stop"}, strings.NewReader("{}"), &errb); code != 0 || errb.Len() != 0 {
		t.Errorf("no env: code %d stderr %q", code, errb.String())
	}
	errb.Reset()
	if code := runHook([]string{"Bogus"}, strings.NewReader("{}"), &errb); code != 0 {
		t.Errorf("bad event: code %d", code)
	}
	if !strings.HasPrefix(errb.String(), "{") || strings.Count(errb.String(), "\n") != 1 || !strings.Contains(errb.String(), "Bogus") {
		t.Errorf("stderr = %q", errb.String())
	}
	errb.Reset()
	t.Setenv("RELAYDECK_URL", "http://127.0.0.1:1")
	t.Setenv("RELAYDECK_AGENT_TOKEN", "supersecrettoken")
	if code := runHook([]string{"Stop"}, strings.NewReader("{}"), &errb); code != 0 || strings.Contains(errb.String(), "supersecrettoken") || errb.Len() == 0 {
		t.Errorf("unreachable daemon: code %d stderr %q", code, errb.String())
	}
}

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"version"}, 0, "dev\n", ""},
		{[]string{"serve", "--bogus"}, 2, "", "flag provided but not defined"},
		{[]string{"serve", "--port", "80"}, 2, "", "--port must be between 1024 and 65535"},
		{[]string{"serve", "extra"}, 2, "", "usage:"},
		{[]string{"nope"}, 2, "", "usage:"},
	}
	for _, tt := range tests {
		var out, errb bytes.Buffer
		if got := run(tt.args, &out, &errb); got != tt.code {
			t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.code)
		}
		if tt.stdout != "" && out.String() != tt.stdout {
			t.Errorf("run(%v) stdout = %q", tt.args, out.String())
		}
		if !strings.Contains(errb.String(), tt.stderr) {
			t.Errorf("run(%v) stderr = %q, want %q", tt.args, errb.String(), tt.stderr)
		}
	}
}

func TestShutdownSignalsIncludeHangupOnUnix(t *testing.T) {
	sigs := shutdownSignals()
	has := func(want os.Signal) bool { return slices.Contains(sigs, want) }
	if !has(os.Interrupt) || !has(syscall.SIGTERM) {
		t.Fatalf("signals = %v, want interrupt and SIGTERM", sigs)
	}
	if runtime.GOOS != "windows" && !has(syscall.SIGHUP) {
		t.Fatalf("signals = %v, want SIGHUP on unix", sigs)
	}
}
