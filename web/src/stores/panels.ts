// The open terminal panels, in open order, and the one that has focus. The
// sidebar and the session picker open panels here; Task 17 renders them.
//
// Read panelStore.panels and panelStore.focused (both reactive, read-only
// from outside) and change them only through the functions.
import { reactive, readonly } from 'vue'

export interface PanelKey {
  ws: string
  folder: string
}

export type OpenResult = { ok: true } | { ok: false; reason: string }

// The grid has at most 2x2 panels (global constraint: max 4 terminal panels).
export const MAX_PANELS = 4
export const tooManyPanels = `At most ${MAX_PANELS} panels can be open. Close one first.`

export function createPanels() {
  const list = reactive<PanelKey[]>([])
  const state = reactive<{ focused: PanelKey | null }>({ focused: null })

  function indexOf(ws: string, folder: string): number {
    return list.findIndex((p) => p.ws === ws && p.folder === folder)
  }

  function openPanel(ws: string, folder: string): OpenResult {
    const i = indexOf(ws, folder)
    if (i >= 0) {
      state.focused = list[i]
      return { ok: true }
    }
    if (list.length >= MAX_PANELS) return { ok: false, reason: tooManyPanels }
    list.push({ ws, folder })
    state.focused = list[list.length - 1]
    return { ok: true }
  }

  function closePanel(ws: string, folder: string): void {
    const i = indexOf(ws, folder)
    if (i < 0) return
    list.splice(i, 1)
    const f = state.focused
    if (f && f.ws === ws && f.folder === folder) state.focused = list[list.length - 1] ?? null
  }

  // focusPanel only focuses a panel that is open.
  function focusPanel(ws: string, folder: string): void {
    const i = indexOf(ws, folder)
    if (i >= 0) state.focused = list[i]
  }

  // prune closes every panel whose folder keep says is gone (a folder or
  // workspace was removed, #116).
  function prune(keep: (ws: string, folder: string) => boolean): void {
    for (const p of [...list]) {
      if (!keep(p.ws, p.folder)) closePanel(p.ws, p.folder)
    }
  }

  const panels = readonly(list) as readonly Readonly<PanelKey>[]

  return {
    panels,
    // A read-only view, so a caller cannot change focus or the list through it.
    get focused(): Readonly<PanelKey> | null {
      return state.focused ? readonly(state.focused) : null
    },
    openPanel,
    closePanel,
    focusPanel,
    prune,
  }
}

export type Panels = ReturnType<typeof createPanels>

export const panelStore = createPanels()
