import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import LayoutSwitcher from './LayoutSwitcher.vue'

let slot: HTMLElement

beforeEach(() => {
  slot = document.createElement('div')
  slot.id = 'layout-slot'
  document.body.appendChild(slot)
})

afterEach(() => {
  document.body.innerHTML = ''
})

async function mountSwitcher(count: number, layout: number) {
  const w = mount(LayoutSwitcher, { props: { count, layout }, attachTo: document.body })
  await nextTick() // Teleport defer resolves its target after mount
  return w
}

function buttons(): HTMLButtonElement[] {
  return [...slot.querySelectorAll<HTMLButtonElement>('button.lay')]
}

describe('LayoutSwitcher', () => {
  it('renders four labelled layout buttons inside #layout-slot', async () => {
    const w = await mountSwitcher(4, 4)
    expect(slot.querySelector('[role="group"]')?.getAttribute('aria-label')).toBe('Layout')
    expect(buttons().map((b) => b.getAttribute('aria-label'))).toEqual([
      '1 panel, full',
      '2 panels, side by side',
      '3 panels, left full height, right split',
      '4 panels, 2 by 2',
    ])
    w.unmount()
  })

  it('marks the current layout as pressed', async () => {
    const w = await mountSwitcher(3, 2)
    expect(buttons().map((b) => b.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'false', 'false'])
    w.unmount()
  })

  it('disables layouts larger than the panel count', async () => {
    const w = await mountSwitcher(2, 2)
    expect(buttons().map((b) => b.disabled)).toEqual([false, false, true, true])
    w.unmount()
  })

  it('emits pick with the chosen panel count', async () => {
    const w = await mountSwitcher(4, 4)
    buttons()[0].click()
    buttons()[2].click()
    expect(w.emitted('pick')).toEqual([[1], [3]])
    w.unmount()
  })

  it('shows the open and shown count for the phone view', async () => {
    const w = await mountSwitcher(3, 3)
    expect(slot.querySelector('.count-only')?.textContent).toBe('3 panels open')
    await w.setProps({ layout: 1 })
    expect(slot.querySelector('.count-only')?.textContent).toBe('3 panels open, 1 shown')
    await w.setProps({ count: 1 })
    expect(slot.querySelector('.count-only')?.textContent).toBe('1 panel open')
    w.unmount()
  })
})
