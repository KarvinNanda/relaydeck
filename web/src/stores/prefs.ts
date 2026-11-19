// Layout preferences of this browser: sidebar width or hidden, terminal font
// size and whether the Messages drawer is open. Kept in localStorage, which
// can be missing, blocked or hold anything, so every access is guarded and
// every field falls back to its default on its own.
import { reactive, watch } from 'vue'

export const KEY = 'relaydeck.prefs.v1'
export const SIDE_MIN = 160
export const SIDE_MAX = 420
export const SIDE_DEFAULT = 232
export const FONT_MIN = 10
export const FONT_MAX = 16
export const FONT_DEFAULT = 12

export interface Prefs {
  sideWidth: number
  sideHidden: boolean
  termFont: number
  messagesOpen: boolean
}

function defaults(): Prefs {
  return { sideWidth: SIDE_DEFAULT, sideHidden: false, termFont: FONT_DEFAULT, messagesOpen: false }
}

export const prefs = reactive<Prefs>(defaults())

let storage: Storage | null = null

function inRange(v: unknown, min: number, max: number): v is number {
  return typeof v === 'number' && Number.isInteger(v) && v >= min && v <= max
}

// loadPrefs reads the stored preferences into prefs. Tests pass their own
// storage; the app passes window.localStorage (see main.ts).
export function loadPrefs(s: Storage | null): void {
  storage = s
  let raw: unknown = null
  try {
    const text = s?.getItem(KEY)
    raw = text ? JSON.parse(text) : null
  } catch {
    raw = null
  }
  const v = raw !== null && typeof raw === 'object' && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {}
  const d = defaults()
  prefs.sideWidth = inRange(v.sideWidth, SIDE_MIN, SIDE_MAX) ? v.sideWidth : d.sideWidth
  prefs.sideHidden = typeof v.sideHidden === 'boolean' ? v.sideHidden : d.sideHidden
  prefs.termFont = inRange(v.termFont, FONT_MIN, FONT_MAX) ? v.termFont : d.termFont
  prefs.messagesOpen = typeof v.messagesOpen === 'boolean' ? v.messagesOpen : d.messagesOpen
}

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, Math.round(n)))
}

export function setSideWidth(px: number): void {
  prefs.sideWidth = clamp(px, SIDE_MIN, SIDE_MAX)
}

export function setTermFont(px: number): void {
  prefs.termFont = clamp(px, FONT_MIN, FONT_MAX)
}

// Writes only the fields that changed, over what is stored now: another tab
// may have saved a different field since this one loaded (#81).
watch(
  () => ({ ...prefs }),
  (p, old) => {
    if (!storage) return
    try {
      const text = storage.getItem(KEY)
      let stored: Record<string, unknown> = {}
      try {
        const v: unknown = text ? JSON.parse(text) : null
        if (v !== null && typeof v === 'object' && !Array.isArray(v)) stored = v as Record<string, unknown>
      } catch {
        stored = {}
      }
      for (const k of Object.keys(p) as (keyof Prefs)[]) {
        if (p[k] !== old[k] || !(k in stored)) stored[k] = p[k]
      }
      storage.setItem(KEY, JSON.stringify(stored))
    } catch {
      // blocked storage: the choice lasts until the page reloads
    }
  },
)
