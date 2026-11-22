// Claude Code draws a submitted user prompt on a #373737 bar, secondary text
// in #999 and box rules in #888, all as truecolor SGR, so xterm's 16-color
// palette cannot change them. On relaydeck's dark navy terminal the prompt
// bar almost disappears and the transcript reads flat. Recolor rewrites
// those exact sequences in the output stream before xterm draws it:
//
//   prompt bar  48;2;55;55;55   -> #1E3A5F, and its leading ❯ in blue
//   secondary   38;2;153;153;153 -> slate #94A3B8
//
// Box rules (#888) stay as they are: the same color draws the folder label on
// the rule, and a darker rule made that label unreadable.
//
// Only these standalone sequences are touched. Anything else passes through,
// so if Claude Code changes its colors the panel simply looks as it did. The
// daemon reads the raw PTY bytes, so its screen detector is not affected.

const E = '\x1b['
export const PROMPT_BG = 0x1e3a5f
const PROMPT_IN = `${E}48;2;55;55;55m`
const PROMPT_OUT = `${E}48;2;30;58;95m`

// Longest first: the prompt bar with its ❯ before the bare bar.
const MAP: [string, string][] = [
  [`${PROMPT_IN}❯`, `${PROMPT_OUT}${E}38;2;96;165;250m❯${E}39m`],
  [PROMPT_IN, PROMPT_OUT],
  [`${E}38;2;153;153;153m`, `${E}38;2;148;163;184m`],
]

export class Recolor {
  private dec = new TextDecoder('utf-8')
  private carry = ''

  // push takes one output frame and returns the text to write to xterm. A
  // sequence cut by the frame edge (or a prompt bar whose ❯ has not arrived)
  // is held back and finished with the next frame.
  push(frame: Uint8Array | string): string {
    let s = this.carry + (typeof frame === 'string' ? frame : this.dec.decode(frame, { stream: true }))
    this.carry = ''
    const esc = s.lastIndexOf('\x1b')
    if (esc >= 0 && /^\x1b(\[[0-9;:?]*)?$/.test(s.slice(esc))) {
      this.carry = s.slice(esc)
      s = s.slice(0, esc)
    } else if (s.endsWith(PROMPT_IN)) {
      this.carry = PROMPT_IN
      s = s.slice(0, -PROMPT_IN.length)
    }
    for (const [from, to] of MAP) s = s.split(from).join(to)
    return s
  }
}

interface CellLike {
  isBgRGB(): boolean
  getBgColor(): number
}
interface BufferLike {
  viewportY: number
  getLine(y: number): { getCell(x: number): CellLike | undefined } | undefined
}

// userRows lists the visible rows (0 = top of the viewport) that start with
// the recolored prompt background: the rows of submitted user prompts.
export function userRows(buf: BufferLike, rows: number): number[] {
  const out: number[] = []
  for (let r = 0; r < rows; r++) {
    const c = buf.getLine(buf.viewportY + r)?.getCell(0)
    if (c && c.isBgRGB() && c.getBgColor() === PROMPT_BG) out.push(r)
  }
  return out
}
