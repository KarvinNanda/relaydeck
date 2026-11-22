import { describe, expect, it } from 'vitest'
import { detectOS, keyAction, type TermOS } from './termkeys'

describe('detectOS', () => {
  it('reads userAgentData.platform first', () => {
    expect(detectOS({ userAgentData: { platform: 'Windows' } })).toBe('windows')
    expect(detectOS({ userAgentData: { platform: 'macOS' } })).toBe('mac')
    expect(detectOS({ userAgentData: { platform: 'Linux' } })).toBe('linux')
  })

  it('falls back to navigator.platform', () => {
    expect(detectOS({ platform: 'Win32' })).toBe('windows')
    expect(detectOS({ platform: 'MacIntel' })).toBe('mac')
    expect(detectOS({ platform: 'Linux x86_64' })).toBe('linux')
  })

  it('falls back when userAgentData.platform is empty', () => {
    expect(detectOS({ userAgentData: { platform: '' }, platform: 'Win32' })).toBe('windows')
  })

  it('treats an unknown platform as linux', () => {
    expect(detectOS({})).toBe('linux')
  })
})

type Mods = { ctrl?: boolean; shift?: boolean; alt?: boolean; meta?: boolean; type?: string; keyCode?: number; composing?: boolean }

function k(code: string, m: Mods = {}) {
  return {
    type: m.type ?? 'keydown',
    code,
    ctrlKey: !!m.ctrl,
    shiftKey: !!m.shift,
    altKey: !!m.alt,
    metaKey: !!m.meta,
    keyCode: m.keyCode ?? 0,
    isComposing: !!m.composing,
  }
}

function act(os: TermOS, code: string, m: Mods, sel: boolean) {
  return keyAction(os, k(code, m), sel)
}

describe('keyAction on windows', () => {
  it('copies on Ctrl+C only when text is selected', () => {
    expect(act('windows', 'KeyC', { ctrl: true }, true)).toBe('copy')
    expect(act('windows', 'KeyC', { ctrl: true }, false)).toBe('pass')
  })

  it('copies on Ctrl+Shift+C even with no selection', () => {
    expect(act('windows', 'KeyC', { ctrl: true, shift: true }, false)).toBe('copy')
  })

  it('pastes on Ctrl+V and Ctrl+Shift+V', () => {
    expect(act('windows', 'KeyV', { ctrl: true }, false)).toBe('paste')
    expect(act('windows', 'KeyV', { ctrl: true, shift: true }, false)).toBe('paste')
  })

  it('leaves AltGr (Ctrl+Alt) combos alone', () => {
    expect(act('windows', 'KeyC', { ctrl: true, alt: true }, true)).toBe('pass')
    expect(act('windows', 'KeyV', { ctrl: true, alt: true }, false)).toBe('pass')
  })

  it('leaves combos with the Windows key alone', () => {
    expect(act('windows', 'KeyV', { ctrl: true, meta: true }, false)).toBe('pass')
  })

  it('gives keyup and keypress the same answer as keydown', () => {
    expect(act('windows', 'KeyC', { ctrl: true, type: 'keyup' }, true)).toBe('copy')
    expect(act('windows', 'KeyC', { ctrl: true, type: 'keypress' }, true)).toBe('copy')
  })

  it('leaves other Ctrl keys alone', () => {
    expect(act('windows', 'KeyX', { ctrl: true }, true)).toBe('pass')
  })
})

describe('keyAction on linux', () => {
  it('copies and pastes with Ctrl+Shift', () => {
    expect(act('linux', 'KeyC', { ctrl: true, shift: true }, false)).toBe('copy')
    expect(act('linux', 'KeyV', { ctrl: true, shift: true }, false)).toBe('paste')
  })

  it('sends plain Ctrl+C and Ctrl+V to the agent', () => {
    expect(act('linux', 'KeyC', { ctrl: true }, true)).toBe('pass')
    expect(act('linux', 'KeyV', { ctrl: true }, false)).toBe('pass')
  })
})

describe('keyAction on mac', () => {
  it('leaves every combo to the browser and xterm', () => {
    expect(act('mac', 'KeyC', { meta: true }, true)).toBe('pass')
    expect(act('mac', 'KeyV', { meta: true }, false)).toBe('pass')
    expect(act('mac', 'KeyC', { ctrl: true }, true)).toBe('pass')
    expect(act('mac', 'KeyV', { ctrl: true, shift: true }, false)).toBe('pass')
  })
})

describe('keyAction follows the keyboard layout like xterm (#67)', () => {
  it('copies on the key labelled C in Dvorak (code KeyI, keyCode 67)', () => {
    expect(act('windows', 'KeyI', { ctrl: true, keyCode: 67 }, true)).toBe('copy')
    expect(act('linux', 'KeyI', { ctrl: true, shift: true, keyCode: 67 }, false)).toBe('copy')
  })

  it('pastes on the key labelled V in Dvorak (code Period, keyCode 86)', () => {
    expect(act('windows', 'Period', { ctrl: true, keyCode: 86 }, false)).toBe('paste')
  })

  it('leaves the physical C key alone when the layout makes it another letter', () => {
    // Dvorak: the physical C position types J, so Ctrl+that key is Ctrl+J
    expect(act('windows', 'KeyC', { ctrl: true, keyCode: 74 }, true)).toBe('pass')
  })

  it('still copies on a Cyrillic layout, where keyCode stays 67', () => {
    expect(act('windows', 'KeyC', { ctrl: true, keyCode: 67 }, true)).toBe('copy')
  })
})

describe('keyAction newline on every OS', () => {
  const all: TermOS[] = ['mac', 'windows', 'linux']

  it('makes Shift+Enter and Ctrl+Enter a newline', () => {
    for (const os of all) {
      expect(act(os, 'Enter', { shift: true, keyCode: 13 }, false)).toBe('newline')
      expect(act(os, 'Enter', { ctrl: true, keyCode: 13 }, false)).toBe('newline')
      expect(act(os, 'Enter', { ctrl: true, shift: true, keyCode: 13 }, false)).toBe('newline')
    }
  })

  it('also takes the numpad Enter, and an Enter known only by keyCode', () => {
    for (const os of all) {
      expect(act(os, 'NumpadEnter', { shift: true, keyCode: 13 }, false)).toBe('newline')
      expect(act(os, '', { shift: true, keyCode: 13 }, false)).toBe('newline')
    }
  })

  it('leaves plain Enter, and Enter with Alt or Meta, to xterm', () => {
    for (const os of all) {
      expect(act(os, 'Enter', { keyCode: 13 }, false)).toBe('pass')
      expect(act(os, 'Enter', { alt: true, keyCode: 13 }, false)).toBe('pass')
      expect(act(os, 'Enter', { shift: true, alt: true, keyCode: 13 }, false)).toBe('pass')
      expect(act(os, 'Enter', { shift: true, meta: true, keyCode: 13 }, false)).toBe('pass')
    }
  })

  it('leaves Enter to the IME while it composes', () => {
    for (const os of all) {
      expect(act(os, 'Enter', { shift: true, keyCode: 229 }, false)).toBe('pass')
      expect(act(os, 'Enter', { shift: true, keyCode: 13, composing: true }, false)).toBe('pass')
    }
  })

  it('gives keyup and keypress the same answer as keydown', () => {
    expect(act('linux', 'Enter', { shift: true, keyCode: 13, type: 'keyup' }, false)).toBe('newline')
    expect(act('windows', 'Enter', { ctrl: true, keyCode: 13, type: 'keypress' }, false)).toBe('newline')
  })
})
