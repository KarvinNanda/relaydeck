import { describe, expect, it } from 'vitest'
import { PROMPT_BG, Recolor, userRows } from './termcolors'

const enc = (s: string) => new TextEncoder().encode(s)
const E = '\x1b['

describe('Recolor', () => {
  it('turns the user prompt bar blue and its ❯ blue', () => {
    const r = new Recolor()
    expect(r.push(enc(`${E}48;2;55;55;55m❯ fix the login${E}49m`))).toBe(
      `${E}48;2;30;58;95m${E}38;2;96;165;250m❯${E}39m fix the login${E}49m`,
    )
  })

  it('maps secondary text and box rules to the app colors', () => {
    const r = new Recolor()
    expect(r.push(enc(`${E}38;2;153;153;153mRan 1 shell command${E}39m ${E}38;2;136;136;136m───`))).toBe(
      `${E}38;2;148;163;184mRan 1 shell command${E}39m ${E}38;2;71;85;105m───`,
    )
  })

  it('leaves every other sequence and the agent text alone', () => {
    const r = new Recolor()
    const s = `${E}1;48;2;55;55;55mbold combo${E}38;2;215;119;87m✻ Nesting…${E}0m ● answer`
    expect(r.push(enc(s))).toBe(s)
  })

  it('handles a sequence split across two frames', () => {
    const r = new Recolor()
    const a = r.push(enc(`hello ${E}38;2;153;15`))
    const b = r.push(enc(`3;153mgrey`))
    expect(a + b).toBe(`hello ${E}38;2;148;163;184mgrey`)
  })

  it('still colors ❯ when the frame ends right after the prompt background', () => {
    const r = new Recolor()
    const a = r.push(enc(`x${E}48;2;55;55;55m`))
    const b = r.push(enc('❯ next'))
    expect(a + b).toBe(`x${E}48;2;30;58;95m${E}38;2;96;165;250m❯${E}39m next`)
  })

  it('keeps a multi-byte character split across frames', () => {
    const r = new Recolor()
    const bytes = enc('● done')
    expect(r.push(bytes.slice(0, 2)) + r.push(bytes.slice(2))).toBe('● done')
  })

  it('accepts string frames too', () => {
    expect(new Recolor().push(`${E}48;2;55;55;55m`)).toBe('')
  })
})

// A fake of the slice of the xterm buffer API that userRows reads.
function buffer(rows: { bg: number[] }[], viewportY = 0) {
  const cell = (bg: number) => ({ isBgRGB: () => bg >= 0, getBgColor: () => bg })
  return {
    viewportY,
    getLine: (y: number) => {
      const r = rows[y - viewportY]
      return r && { getCell: (x: number) => (x < r.bg.length ? cell(r.bg[x]) : undefined) }
    },
  }
}

describe('userRows', () => {
  it('finds the visible rows that start with the prompt background', () => {
    const b = buffer([{ bg: [-1, -1] }, { bg: [PROMPT_BG, PROMPT_BG] }, { bg: [0x373737, 0x373737] }, { bg: [PROMPT_BG] }])
    expect(userRows(b, 4)).toEqual([1, 3])
  })

  it('reads from the viewport, not from the top of the buffer', () => {
    const b = buffer([{ bg: [PROMPT_BG] }, { bg: [-1] }], 40)
    expect(userRows(b, 2)).toEqual([0])
  })
})
