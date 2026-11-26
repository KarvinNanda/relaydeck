import { describe, expect, it } from 'vitest'
import { effect } from 'vue'
import { createPanels, MAX_PANELS, tooManyPanels } from './panels'

describe('panels store', () => {
  it('opens panels in order and focuses the new one', () => {
    const p = createPanels()
    expect(p.openPanel('w', 'be')).toEqual({ ok: true })
    expect(p.openPanel('w', 'fe')).toEqual({ ok: true })
    expect(p.panels).toEqual([
      { ws: 'w', folder: 'be' },
      { ws: 'w', folder: 'fe' },
    ])
    expect(p.focused).toEqual({ ws: 'w', folder: 'fe' })
  })

  it('opening an open panel focuses it and does not add it again', () => {
    const p = createPanels()
    p.openPanel('w', 'be')
    p.openPanel('w', 'fe')
    expect(p.openPanel('w', 'be')).toEqual({ ok: true })
    expect(p.panels).toHaveLength(2)
    expect(p.focused).toEqual({ ws: 'w', folder: 'be' })
  })

  it('tells folders of the same name in different workspaces apart', () => {
    const p = createPanels()
    p.openPanel('a', 'be')
    p.openPanel('b', 'be')
    expect(p.panels).toHaveLength(2)
  })

  it('refuses a 5th panel with the exact reason', () => {
    const p = createPanels()
    for (let i = 0; i < MAX_PANELS; i++) expect(p.openPanel('w', `f${i}`).ok).toBe(true)
    expect(MAX_PANELS).toBe(4)
    expect(p.openPanel('w', 'f4')).toEqual({ ok: false, reason: 'At most 4 panels can be open. Close one first.' })
    expect(tooManyPanels).toBe('At most 4 panels can be open. Close one first.')
    expect(p.panels).toHaveLength(4)
    expect(p.focused).toEqual({ ws: 'w', folder: 'f3' })
    // an open one is still focused when the grid is full
    expect(p.openPanel('w', 'f0')).toEqual({ ok: true })
    expect(p.focused).toEqual({ ws: 'w', folder: 'f0' })
  })

  it('closing the focused panel moves focus to the last open one', () => {
    const p = createPanels()
    p.openPanel('w', 'a')
    p.openPanel('w', 'b')
    p.openPanel('w', 'c')
    p.focusPanel('w', 'b')
    p.closePanel('w', 'b')
    expect(p.panels).toEqual([
      { ws: 'w', folder: 'a' },
      { ws: 'w', folder: 'c' },
    ])
    expect(p.focused).toEqual({ ws: 'w', folder: 'c' })
    p.closePanel('w', 'a')
    expect(p.focused).toEqual({ ws: 'w', folder: 'c' })
    p.closePanel('w', 'c')
    expect(p.focused).toBeNull()
    expect(p.panels).toEqual([])
  })

  it('closing a panel that is not open changes nothing', () => {
    const p = createPanels()
    p.openPanel('w', 'a')
    p.closePanel('w', 'zzz')
    expect(p.panels).toHaveLength(1)
    expect(p.focused).toEqual({ ws: 'w', folder: 'a' })
  })

  it('focusPanel ignores a panel that is not open', () => {
    const p = createPanels()
    p.openPanel('w', 'a')
    p.focusPanel('w', 'nope')
    expect(p.focused).toEqual({ ws: 'w', folder: 'a' })
  })

  it('panels and focused are reactive, and panels cannot be changed from outside', () => {
    const p = createPanels()
    let seen = -1
    let focus = ''
    effect(() => {
      seen = p.panels.length
      focus = p.focused?.folder ?? ''
    })
    p.openPanel('w', 'a')
    expect(seen).toBe(1)
    expect(focus).toBe('a')
    ;(p.panels as { ws: string; folder: string }[]).push({ ws: 'x', folder: 'y' })
    expect(p.panels).toHaveLength(1)
  })

  it('focused is a read-only view: changing it changes neither focus nor the list', () => {
    const p = createPanels()
    p.openPanel('w', 'a')
    const f = p.focused as { ws: string; folder: string }
    f.folder = 'changed'
    expect(p.focused).toEqual({ ws: 'w', folder: 'a' })
    expect(p.panels).toEqual([{ ws: 'w', folder: 'a' }])
  })

  it('prune closes the panels of folders that are gone and keeps focus valid (#116)', () => {
    const p = createPanels()
    p.openPanel('w', 'a')
    p.openPanel('w', 'b')
    p.openPanel('x', 'c')
    p.focusPanel('w', 'b')
    p.prune((ws, folder) => !(ws === 'w' && folder === 'b') && ws !== 'x')
    expect(p.panels).toEqual([{ ws: 'w', folder: 'a' }])
    expect(p.focused).toEqual({ ws: 'w', folder: 'a' })
  })
})
