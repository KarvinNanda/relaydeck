package server

import "testing"

// queryReplyRe must match frames made only of terminal query replies, and
// nothing a user could type.
func TestQueryReplyRe(t *testing.T) {
	drop := map[string]string{
		"cursor position":      "\x1b[12;40R",
		"DA1":                  "\x1b[?1;2c",
		"device status":        "\x1b[0n",
		"DA2":                  "\x1b[>0;276;0c",
		"DECRQM":               "\x1b[?2026;2$y",
		"DECRQM ansi":          "\x1b[4;2$y",
		"DCS ST":               "\x1bP1$r0m\x1b\\",
		"DCS XTGETTCAP":        "\x1bP1+r544e=787465726d\x1b\\",
		"OSC 10 BEL":           "\x1b]10;rgb:cccc/cccc/cccc\a",
		"OSC 11 ST":            "\x1b]11;rgb:0f0f/1717/2a2a\x1b\\",
		"OSC 12 BEL":           "\x1b]12;rgb:ffff/ffff/ffff\a",
		"several in one frame": "\x1b[1;1R\x1b[>0;276;0c\x1b[?2026;2$y\x1b]11;rgb:0000/0000/0000\a",
	}
	for name, in := range drop {
		if !queryReplyRe.MatchString(in) {
			t.Errorf("%s: %q should be dropped", name, in)
		}
	}
	keep := map[string]string{
		"typing":            "hi\r",
		"arrow key":         "\x1b[A",
		"reply plus typed":  "\x1b[1;1Rhi",
		"empty":             "",
		"OSC 8 hyperlink":   "\x1b]8;;http://x\a",
		"OSC 52 clipboard":  "\x1b]52;c;aGk=\a",
		"OSC 13":            "\x1b]13;rgb:0/0/0\a",
		"unterminated DCS":  "\x1bP1$r0m",
		"bare ESC":          "\x1b",
		"alt-x":             "\x1bx",
		"F5":                "\x1b[15~",
		"shift-tab":         "\x1b[Z",
		"focus in":          "\x1b[I",
		"focus out":         "\x1b[O",
		"SGR mouse":         "\x1b[<0;10;5M",
		"bracketed paste":   "\x1b[200~hi\x1b[201~",
		"alt-p":             "\x1bp",
		"DCS no terminator": "\x1bPfoo",
	}
	for name, in := range keep {
		if queryReplyRe.MatchString(in) {
			t.Errorf("%s: %q must pass through", name, in)
		}
	}
}
