import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { MAX_PANELS, panelStore, tooManyPanels } from '../stores/panels'
import PanelGrid from './PanelGrid.vue'

// TerminalPanel needs xterm and a socket; its own test covers it. Here a
// stub shows which folder sits in which cell and can ask to maximize.
const TerminalStub = defineComponent({
  name: 'TerminalPanel',
  props: { ws: String, folder: String, focused: Boolean, maximized: Boolean, canMaximize: Boolean },
  emits: ['maximize', 'restore'],
  setup(props, { emit }) {
    return () =>
      h('div', { class: 'tp', 'data-folder': props.folder, 'data-max': String(props.maximized) }, [
        h('button', { class: 'max', onClick: () => emit('maximize') }),
        h('button', { class: 'restore', onClick: () => emit('restore') }),
      ])
  },
})

let slot: HTMLElement
let w: VueWrapper | null = null

function closeAll(): void {
  for (const p of [...panelStore.panels]) panelStore.closePanel(p.ws, p.folder)
}

function open(...names: string[]): void {
  for (const n of names) expect(panelStore.openPanel('game-lounge', n)).toEqual({ ok: true })
}

async function mountGrid() {
  w = mount(PanelGrid, { attachTo: document.body, global: { stubs: { TerminalPanel: TerminalStub } } })
  await nextTick()
  return w
}

function cells() {
  return w!.findAll('.cell').map((c) => ({ area: c.attributes('data-area'), folder: c.find('.tp').attributes('data-folder') }))
}

function layoutButtons(): HTMLButtonElement[] {
  return [...slot.querySelectorAll<HTMLButtonElement>('button.lay')]
}

beforeEach(() => {
  closeAll()
  slot = document.createElement('div')
  slot.id = 'layout-slot'
  document.body.appendChild(slot)
})

afterEach(() => {
  w?.unmount()
  w = null
  closeAll()
  document.body.innerHTML = ''
})

describe('PanelGrid', () => {
  it('shows the empty state and no layout switcher when no panel is open', async () => {
    await mountGrid()
    expect(w!.text()).toContain('No terminal open')
    expect(w!.find('.grid').exists()).toBe(false)
    expect(layoutButtons()).toHaveLength(0)
  })

  it.each([
    [1, ['a']],
    [2, ['a', 'b']],
    [3, ['a', 'b', 'c']],
    [4, ['a', 'b', 'c', 'd']],
  ])('lays out %i panel(s) in grid areas %j', async (n, areas) => {
    const names = ['be', 'fe-admin', 'fe-customer', 'worker'].slice(0, n)
    open(...names)
    await mountGrid()
    const grid = w!.find('.grid')
    expect(grid.classes()).toContain(`n${n}`)
    expect(cells()).toEqual(names.map((folder, i) => ({ area: areas[i], folder })))
  })

  it('uses the spec 7.8 grid template for each layout', async () => {
    const { templateAreas } = await import('./PanelGrid.vue')
    expect(templateAreas).toEqual({
      1: '"a"',
      2: '"a b"',
      3: '"a b" "a c"', // left full height, right split in two
      4: '"a b" "c d"', // 2x2
    })
  })

  it('refuses a 5th panel and keeps showing 4', async () => {
    open('be', 'fe-admin', 'fe-customer', 'worker')
    await mountGrid()
    expect(panelStore.openPanel('game-lounge', 'extra')).toEqual({ ok: false, reason: tooManyPanels })
    await nextTick()
    expect(panelStore.panels).toHaveLength(MAX_PANELS)
    expect(cells().map((c) => c.folder)).toEqual(['be', 'fe-admin', 'fe-customer', 'worker'])
  })

  it('picks the layout from the panel count', async () => {
    open('be', 'fe-admin')
    await mountGrid()
    expect(layoutButtons().map((b) => b.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'false', 'false'])
    open('fe-customer')
    await nextTick()
    expect(w!.find('.grid').classes()).toContain('n3')
    expect(layoutButtons()[2].getAttribute('aria-pressed')).toBe('true')
  })

  it('shows fewer panels when the user picks a smaller layout, keeping the focused one', async () => {
    open('be', 'fe-admin', 'fe-customer', 'worker') // worker has focus (opened last)
    await mountGrid()
    layoutButtons()[1].click()
    await nextTick()
    expect(w!.find('.grid').classes()).toContain('n2')
    expect(cells()).toEqual([
      { area: 'a', folder: 'be' },
      { area: 'b', folder: 'worker' },
    ])
    panelStore.focusPanel('game-lounge', 'fe-admin')
    await nextTick()
    expect(cells().map((c) => c.folder)).toEqual(['be', 'fe-admin'])
  })

  it('goes back to the automatic layout when the panel count changes', async () => {
    open('be', 'fe-admin', 'fe-customer')
    await mountGrid()
    layoutButtons()[0].click()
    await nextTick()
    expect(cells()).toHaveLength(1)
    open('worker')
    await nextTick()
    expect(w!.find('.grid').classes()).toContain('n4')
    expect(cells()).toHaveLength(4)
  })

  it('maximizes one panel and restores the layout', async () => {
    open('be', 'fe-admin', 'fe-customer')
    await mountGrid()
    await w!.findAll('.tp')[0].find('.max').trigger('click')
    expect(cells()).toEqual([{ area: 'a', folder: 'be' }])
    expect(w!.find('.tp').attributes('data-max')).toBe('true')
    expect(panelStore.focused).toEqual({ ws: 'game-lounge', folder: 'be' })
    await w!.find('.tp .restore').trigger('click')
    expect(cells().map((c) => c.folder)).toEqual(['be', 'fe-admin', 'fe-customer'])
    expect(w!.findAll('.tp').every((t) => t.attributes('data-max') === 'false')).toBe(true)
  })

  it('binds the rendered grid areas from the exported templates', async () => {
    const { templateAreas } = await import('./PanelGrid.vue')
    open('be', 'fe-admin', 'fe-customer')
    await mountGrid()
    const grid = () => w!.find('.grid').element as HTMLElement
    expect(grid().style.getPropertyValue('--areas')).toBe(templateAreas[3])
    layoutButtons()[1].click()
    await nextTick()
    expect(grid().style.getPropertyValue('--areas')).toBe(templateAreas[2])
  })

  describe('on a phone', () => {
    beforeEach(() => {
      vi.stubGlobal('matchMedia', (q: string) => ({
        matches: q.includes('max-width: 767px'),
        addEventListener: () => {},
        removeEventListener: () => {},
      }))
    })
    afterEach(() => vi.unstubAllGlobals())

    it('stacks every open panel whatever layout is picked, with no maximize', async () => {
      open('be', 'fe-admin', 'fe-customer', 'worker')
      await mountGrid()
      layoutButtons()[0].click()
      await nextTick()
      expect(cells().map((c) => c.folder)).toEqual(['be', 'fe-admin', 'fe-customer', 'worker'])
      expect(w!.findAll('.tp').every((t) => t.attributes('data-max') === 'false')).toBe(true)
      expect(slot.querySelector('.count-only')?.textContent).toBe('4 panels open')
    })
  })
})
