import { nextTick } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'
import {
  FONT_DEFAULT,
  FONT_MAX,
  FONT_MIN,
  KEY,
  SIDE_DEFAULT,
  SIDE_MAX,
  SIDE_MIN,
  loadPrefs,
  prefs,
  setSideWidth,
  setTermFont,
} from './prefs'

class MemStorage {
  data = new Map<string, string>()
  getItem(k: string) {
    return this.data.get(k) ?? null
  }
  setItem(k: string, v: string) {
    this.data.set(k, v)
  }
}

function mem(value?: unknown): MemStorage {
  const s = new MemStorage()
  if (value !== undefined) s.setItem(KEY, typeof value === 'string' ? value : JSON.stringify(value))
  return s
}

const throwing = {
  getItem(): string | null {
    throw new Error('blocked')
  },
  setItem(): void {
    throw new Error('blocked')
  },
}

describe('prefs', () => {
  beforeEach(() => loadPrefs(mem() as unknown as Storage))

  it('starts from the defaults with empty storage', () => {
    expect({ ...prefs }).toEqual({ sideWidth: SIDE_DEFAULT, sideHidden: false, termFont: FONT_DEFAULT, messagesOpen: false })
    expect([SIDE_MIN, SIDE_MAX, SIDE_DEFAULT, FONT_MIN, FONT_MAX, FONT_DEFAULT]).toEqual([160, 420, 232, 10, 16, 12])
  })

  it('loads valid stored values', () => {
    loadPrefs(mem({ sideWidth: 300, sideHidden: true, termFont: 10, messagesOpen: true }) as unknown as Storage)
    expect({ ...prefs }).toEqual({ sideWidth: 300, sideHidden: true, termFont: 10, messagesOpen: true })
  })

  it('falls back field by field for bad values', () => {
    loadPrefs(mem({ sideWidth: 9999, sideHidden: 'yes', termFont: 13, messagesOpen: 1 }) as unknown as Storage)
    expect({ ...prefs }).toEqual({ sideWidth: SIDE_DEFAULT, sideHidden: false, termFont: 13, messagesOpen: false })
    loadPrefs(mem('{not json') as unknown as Storage)
    expect(prefs.sideWidth).toBe(SIDE_DEFAULT)
    loadPrefs(mem('[1,2]') as unknown as Storage)
    expect(prefs.termFont).toBe(FONT_DEFAULT)
  })

  it('keeps the defaults when storage throws', async () => {
    expect(() => loadPrefs(throwing as unknown as Storage)).not.toThrow()
    expect(prefs.sideWidth).toBe(SIDE_DEFAULT)
    setSideWidth(300)
    await nextTick()
    expect(prefs.sideWidth).toBe(300)
  })

  it('works with no storage at all', () => {
    expect(() => loadPrefs(null)).not.toThrow()
    expect(prefs.termFont).toBe(FONT_DEFAULT)
  })

  it('clamps and rounds in the setters', () => {
    setSideWidth(10)
    expect(prefs.sideWidth).toBe(SIDE_MIN)
    setSideWidth(5000)
    expect(prefs.sideWidth).toBe(SIDE_MAX)
    setSideWidth(250.6)
    expect(prefs.sideWidth).toBe(251)
    setTermFont(2)
    expect(prefs.termFont).toBe(FONT_MIN)
    setTermFont(40)
    expect(prefs.termFont).toBe(FONT_MAX)
  })

  it('saves a change under the key', async () => {
    const s = mem()
    loadPrefs(s as unknown as Storage)
    setTermFont(11)
    prefs.messagesOpen = true
    await nextTick()
    expect(JSON.parse(s.getItem(KEY)!)).toEqual({ sideWidth: SIDE_DEFAULT, sideHidden: false, termFont: 11, messagesOpen: true })
  })

  it("writes only the changed field, so another tab's choice is kept (#81)", async () => {
    const s = mem()
    loadPrefs(s as unknown as Storage)
    // another tab saved a font size after this tab loaded
    s.setItem(KEY, JSON.stringify({ sideWidth: SIDE_DEFAULT, sideHidden: false, termFont: 10, messagesOpen: false }))
    setSideWidth(300)
    await nextTick()
    const saved = JSON.parse(s.getItem(KEY)!)
    expect(saved.sideWidth).toBe(300)
    expect(saved.termFont).toBe(10)
  })
})
