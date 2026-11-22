// Copy and paste keys in a terminal panel, per the OS the browser runs on.
//
// xterm.js turns Ctrl+C into \x03 and Ctrl+V into \x16 and prevents the
// browser default, so on Windows and Linux the browser never copies or
// pastes. keyAction says which keys the panel takes back from xterm:
//
//   macOS    Cmd+C / Cmd+V stay native; Ctrl+C and Ctrl+V go to the agent.
//   Windows  Ctrl+C copies when text is selected, else it goes to the agent
//            (interrupt). Ctrl+V pastes. Ctrl+Shift+C / Ctrl+Shift+V too.
//   Linux    Ctrl+Shift+C copies, Ctrl+Shift+V pastes. Plain Ctrl+C and
//            Ctrl+V go to the agent.
//
// On every OS, Shift+Enter and Ctrl+Enter are a newline in the input box.
// xterm sends \r for Enter with Shift or Ctrl, which Claude Code submits, so
// the panel sends Meta+Enter (\x1b\r) instead, as Option+Enter does.

export type TermOS = 'mac' | 'windows' | 'linux'
export type KeyAction = 'copy' | 'paste' | 'newline' | 'pass'

type NavLike = { userAgentData?: { platform?: string }; platform?: string }

// The OS of the browser, not of the server: the keys follow the keyboard the
// user types on. Anything unknown counts as linux.
export function detectOS(nav: NavLike): TermOS {
  const p = (nav.userAgentData?.platform || nav.platform || '').toLowerCase()
  if (p.startsWith('win')) return 'windows'
  if (p.startsWith('mac')) return 'mac'
  return 'linux'
}

type KeyLike = Pick<KeyboardEvent, 'type' | 'code' | 'ctrlKey' | 'shiftKey' | 'altKey' | 'metaKey'> & {
  keyCode?: number
  isComposing?: boolean
}

// The letter of a key the way xterm reads it: keyCode follows the keyboard
// layout (Dvorak, and Cyrillic keeps 67 for the key labelled C), so the panel
// takes back exactly the key xterm would turn into a control byte (#67).
// Without a keyCode, the physical key (ev.code) decides. CapsLock does not
// change either.
function letter(ev: KeyLike): string {
  if (ev.keyCode && ev.keyCode >= 65 && ev.keyCode <= 90) return String.fromCharCode(ev.keyCode)
  return ev.code.startsWith('Key') ? ev.code.slice(3) : ''
}

// Enter (main or numpad) with Shift or Ctrl, and no Alt or Meta. While an
// IME composes (keyCode 229), Enter belongs to the IME.
function isNewline(ev: KeyLike): boolean {
  if (ev.isComposing || ev.keyCode === 229) return false
  const enter = ev.code === 'Enter' || ev.code === 'NumpadEnter' || ev.keyCode === 13
  return enter && (ev.shiftKey || ev.ctrlKey) && !ev.altKey && !ev.metaKey
}

// keyAction gives keydown, keypress and keyup of one combo the same answer,
// so xterm never sees any phase of a key the panel took. A combo with
// Alt (AltGr arrives as Ctrl+Alt on Windows) or Meta is never copy or paste.
export function keyAction(os: TermOS, ev: KeyLike, hasSelection: boolean): KeyAction {
  if (isNewline(ev)) return 'newline'
  if (os === 'mac' || !ev.ctrlKey || ev.altKey || ev.metaKey) return 'pass'
  const k = letter(ev)
  if (k === 'C') {
    if (ev.shiftKey) return 'copy'
    return os === 'windows' && hasSelection ? 'copy' : 'pass'
  }
  if (k === 'V') {
    if (ev.shiftKey) return 'paste'
    return os === 'windows' ? 'paste' : 'pass'
  }
  return 'pass'
}
