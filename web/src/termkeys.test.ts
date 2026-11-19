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

type Mods = { ctrl?: boolean; shift?: boolean; alt?: boolean; meta?: boolean; type?: string }

function k(code: string, m: Mods = {}) {
  return {
    type: m.type ?? 'keydown',
    code,
    ctrlKey: !!m.ctrl,
    shiftKey: !!m.shift,
    altKey: !!m.alt,
    metaKey: !!m.meta,
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
