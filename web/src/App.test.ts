import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import App from './App.vue'
import { store } from './stores/state'

afterEach(() => {
  store.state.unauthorized = false
  store.state.connection = 'connecting'
  store.state.data = null
  store.ui.messagesOpen = true
})

describe('App', () => {
  it('shows only the "open the link" screen when unauthorized', () => {
    store.state.unauthorized = true
    const w = mount(App)
    expect(w.text()).toContain('Open relaydeck from the link printed by relaydeck serve.')
    expect(w.find('nav').exists()).toBe(false)
    expect(w.text()).not.toContain('{')
  })

  it('shows the frame: top bar, sidebar, terminals and the Messages drawer', () => {
    store.state.connection = 'connected'
    store.state.data = {
      platform: 'darwin',
      account: { cswap: false, known: false, default: '' },
      workspaces: [{ name: 'game-lounge', folders: [] }],
    }
    const w = mount(App)
    expect(w.find('.top').text()).toContain('relaydeck')
    expect(w.find('.crumb').text()).toContain('game-lounge')
    expect(w.find('[role="status"]').text()).toContain('connected')
    expect(w.find('nav[aria-label="Workspaces"]').exists()).toBe(true)
    expect(w.find('section[aria-label="Terminals"]').exists()).toBe(true)
    expect(w.find('#messages h2').text()).toBe('Messages')
    expect(w.find('#layout-slot').exists()).toBe(true)
  })

  it('the Messages button toggles the drawer', async () => {
    const w = mount(App, { attachTo: document.body })
    const btn = w.find('button[aria-controls="messages"]')
    expect(btn.attributes('aria-pressed')).toBe('true')
    await btn.trigger('click')
    expect(btn.attributes('aria-pressed')).toBe('false')
    expect(w.find('#messages').isVisible()).toBe(false)
    await btn.trigger('click')
    await nextTick()
    expect(w.find('#messages').isVisible()).toBe(true)
    w.unmount()
  })

  it('shows the count of messages awaiting approval on the Messages button', () => {
    store.state.data = {
      platform: 'darwin',
      account: { cswap: false, known: false, default: '' },
      workspaces: [
        {
          name: 'w',
          folders: [
            { name: 'a', path: '/a', permission_mode: 'auto', status: 'idle', on_default_login: false, pending: 0, awaiting_approval: 2 },
            { name: 'b', path: '/b', permission_mode: 'auto', status: 'idle', on_default_login: false, pending: 3, awaiting_approval: 1 },
          ],
        },
      ],
    }
    const w = mount(App)
    expect(w.find('.count').text()).toBe('3')
    expect(w.find('button[aria-controls="messages"]').text()).toContain('3 awaiting approval')
  })

  it('shows disconnected as text, not only color', () => {
    store.state.connection = 'disconnected'
    const w = mount(App)
    expect(w.find('[role="status"]').text()).toContain('disconnected')
    expect(w.find('[role="status"] .dot').classes()).toContain('disconnected')
  })

  it('the drawer collapse button is labelled, says it is expanded, and closes the drawer', async () => {
    const w = mount(App)
    const btn = w.find('#messages button[aria-label="Collapse Messages"]')
    expect(btn.attributes('aria-expanded')).toBe('true')
    await btn.trigger('click')
    expect(store.ui.messagesOpen).toBe(false)
  })
})
