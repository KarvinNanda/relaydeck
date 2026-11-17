package webui

import "testing"

func TestFSOpens(t *testing.T) {
	f, err := FS().Open(".")
	if err != nil {
		t.Fatalf("open embedded dist: %v", err)
	}
	_ = f.Close()
}
