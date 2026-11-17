// Command fakeclaude stands in for Claude Code in daemon and server tests.
//
//   - "fakeclaude agents --json" prints "[]" (no external sessions).
//   - Otherwise it writes <FAKECLAUDE_DIR>/<RELAYDECK_AGENT_ID>.json with its
//     argv, cwd and the RELAYDECK_* variables, then draws a Claude Code style
//     input box (120x40) that the screen detector reads as an empty prompt.
//   - Every input line is appended to <FAKECLAUDE_DIR>/<id>.lines and the box
//     is drawn again. "/exit" exits 0, unless FAKECLAUDE_IGNORE_EXIT is set.
//     "noprompt" clears the screen and leaves the box away (an overlay).
//     "trust" draws the box plus the trust dialog text. "busy" draws the box
//     with "esc to interrupt" in the footer.
//   - FAKECLAUDE_EXIT_AT_ONCE set: exit 1 right after writing the info file
//     (a session that cannot be resumed, a crash on start).
//
// The PTY stays in canonical mode, so typed or pasted text is echoed into the
// input box by the terminal itself, which the detector reads as a draft.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agents" {
		fmt.Println("[]")
		return
	}
	dir := os.Getenv("FAKECLAUDE_DIR")
	id := os.Getenv("RELAYDECK_AGENT_ID")
	if dir != "" && id != "" {
		wd, _ := os.Getwd()
		info := map[string]any{
			"argv":  os.Args[1:],
			"cwd":   wd,
			"url":   os.Getenv("RELAYDECK_URL"),
			"id":    id,
			"token": os.Getenv("RELAYDECK_AGENT_TOKEN"),
		}
		b, _ := json.Marshal(info)
		tmp := filepath.Join(dir, id+".tmp")
		if err := os.WriteFile(tmp, b, 0o600); err == nil {
			_ = os.Rename(tmp, filepath.Join(dir, id+".json"))
		}
	}
	if os.Getenv("FAKECLAUDE_EXIT_AT_ONCE") != "" {
		os.Exit(1)
	}
	draw()
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		if dir != "" && id != "" {
			if f, err := os.OpenFile(filepath.Join(dir, id+".lines"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				fmt.Fprintln(f, line)
				f.Close()
			}
		}
		if line == "/exit" && os.Getenv("FAKECLAUDE_IGNORE_EXIT") == "" {
			os.Exit(0)
		}
		switch line {
		case "noprompt":
			fmt.Print("\x1b[2J\x1b[1;1Hsome overlay")
			continue
		case "trust":
			draw()
			fmt.Print("\x1b[5;1H > 1. Yes, I trust this folder\x1b[38;3H")
			continue
		case "busy":
			draw()
			fmt.Print("\x1b[40;1H  esc to interrupt\x1b[38;3H")
			continue
		}
		draw()
	}
}

// draw paints rule, prompt row, rule and footer on the last four rows of a
// 120x40 screen and leaves the cursor after the prompt mark.
func draw() {
	rule := strings.Repeat("─", 120)
	fmt.Print("\x1b[2J\x1b[37;1H" + rule + "\x1b[38;1H❯ \x1b[39;1H" + rule +
		"\x1b[40;1H  ? for shortcuts\x1b[38;3H")
}
