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
    expect(w.find('button.smaller').attributes('aria-label')).toBe('Smaller terminal text, now 13px')
    expect(w.find('button.bigger').attributes('aria-label')).toBe('Bigger terminal text, now 13px')
  })

  it('disables the buttons at the limits', async () => {
    prefs.termFont = 10
    const w = mount(FontSize)
    expect(w.find('button.smaller').attributes('disabled')).toBeDefined()
    prefs.termFont = 16
    await w.vm.$nextTick()
    expect(w.find('button.bigger').attributes('disabled')).toBeDefined()
    expect(w.find('button.smaller').attributes('disabled')).toBeUndefined()
  })
})
