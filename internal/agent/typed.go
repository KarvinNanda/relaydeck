package agent

import "regexp"

// neutralInputRe matches a write made only of terminal chatter that is not the
// user touching the keyboard: xterm focus reports, mouse reports (SGR and
// legacy X10, whose three bytes must be printable) and replies to terminal queries. The query replies mirror
// queryReplyRe in internal/server/ws.go: cursor position, device status, DA1
// and DA2, DECRQM mode reports, DCS replies and OSC 10 to 12 color reports.
//
// Everything else counts as the user touching the input: text, paste, Enter,
// Esc, arrows, Ctrl and Alt keys. A write that does not match as a whole
// (including a sequence cut short) counts as touched, so every error falls
// toward "the user is typing", which holds a message.
var neutralInputRe = regexp.MustCompile(`(?s)^(?:` +
	`\x1b\[[IO]` +
	`|\x1b\[<[0-9;]*[Mm]` +
	`|\x1b\[M[ -~\x{a0}-\x{10ffff}]{3}` +
	`|\x1b\[[?>]?[0-9;]*[Rcn]` +
	`|\x1b\[\??[0-9;]*\$y` +
	`|\x1bP[^\x1b\x07]*\x1b\\` +
	`|\x1b\]1[0-2];[^\x1b\x07]*(?:\x07|\x1b\\)` +
	`)+$`)

// neutralOnly reports whether p holds only neutral sequences. No parser state
// carries over between writes.
func neutralOnly(p []byte) bool { return neutralInputRe.Match(p) }
