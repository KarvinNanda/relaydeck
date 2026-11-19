import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import FontSize from './FontSize.vue'
import { loadPrefs, prefs } from '../stores/prefs'

afterEach(() => loadPrefs(null))

describe('FontSize', () => {
  it('shows the size and changes it one pixel at a time', async () => {
    const w = mount(FontSize)
    expect(w.find('[role="group"]').attributes('aria-label')).toBe('Terminal text size')
    expect(w.find('.v').text()).toBe('12px')
    await w.find('button.smaller').trigger('click')
    expect(prefs.termFont).toBe(11)
    expect(w.find('.v').text()).toBe('11px')
    await w.find('button.bigger').trigger('click')
    await w.find('button.bigger').trigger('click')
    expect(prefs.termFont).toBe(13)
    expect(w.find('button.smaller').attributes('aria-label')).toBe('Smaller terminal text')
    expect(w.find('button.bigger').attributes('aria-label')).toBe('Bigger terminal text')
    // the size is announced by a polite live region, not by the focused label
    expect(w.find('.v').attributes('aria-live')).toBe('polite')
    expect(w.find('.v').attributes('aria-hidden')).toBeUndefined()
  })

  it('marks the buttons at the limits without dropping keyboard focus (#81)', async () => {
    prefs.termFont = 10
    const w = mount(FontSize)
    const smaller = w.find('button.smaller')
    expect(smaller.attributes('aria-disabled')).toBe('true')
    expect(smaller.attributes('disabled')).toBeUndefined()
    await smaller.trigger('click')
    expect(prefs.termFont).toBe(10)
    prefs.termFont = 16
    await w.vm.$nextTick()
    expect(w.find('button.bigger').attributes('aria-disabled')).toBe('true')
    expect(w.find('button.smaller').attributes('aria-disabled')).toBe('false')
  })
})
