package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"version"}, 0, "dev\n", ""},
		{nil, 1, "", "not implemented"},
		{[]string{"serve", "--port", "1"}, 1, "", "not implemented"},
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
