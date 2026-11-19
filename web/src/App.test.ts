import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import App from './App.vue'
import { store } from './stores/state'
import { loadPrefs, prefs } from './stores/prefs'

afterEach(() => {
  store.state.unauthorized = false
  store.state.connection = 'connecting'
  store.state.data = null
  store.ui.messagesOpen = true
  loadPrefs(null)
})

describe('App', () => {
  it('shows only the "open the link" screen when unauthorized', () => {
    store.state.unauthorized = true
    const w = mount(App)
    expect(w.text()).toContain('Open relaydeck from the link printed by relaydeck serve.')
    // A restart changes the token, so the screen says how to get a fresh link.
    expect(w.text()).toContain('Run relaydeck open in a terminal to get a fresh link.')
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

  it('a file dropped outside any panel never opens in the tab', () => {
    store.state.connection = 'connected'
    store.state.data = { platform: 'darwin', account: { cswap: false, known: false, default: '' }, workspaces: [] }
    const w = mount(App, { attachTo: document.body })
    for (const type of ['dragover', 'drop']) {
      const ev = new Event(type, { bubbles: true, cancelable: true })
      Object.defineProperty(ev, 'dataTransfer', { value: { types: ['Files'], files: [] } })
      document.body.dispatchEvent(ev)
      expect(ev.defaultPrevented).toBe(true)
    }
    w.unmount()
  })

  describe('sidebar width', () => {
    function frame() {
      store.state.connection = 'connected'
      store.state.data = { platform: 'darwin', account: { cswap: false, known: false, default: '' }, workspaces: [] }
      return mount(App, { attachTo: document.body })
    }
    const cols = (w: ReturnType<typeof mount>) => (w.find('.app').element as HTMLElement).style.gridTemplateColumns

    it('uses the saved width and 0 when the sidebar is hidden', async () => {
      prefs.sideWidth = 300
      const w = frame()
      expect(cols(w)).toMatch(/^300px /)
      const hide = w.find('button.side-toggle')
      expect(hide.attributes('aria-label')).toBe('Hide sidebar')
      // a disclosure, not a toggle: expanded while the sidebar shows (#76)
      expect(hide.attributes('aria-expanded')).toBe('true')
      expect(hide.attributes('aria-controls')).toBe('sidebar')
      expect(hide.attributes('aria-pressed')).toBeUndefined()
      expect(w.find('nav#sidebar').exists()).toBe(true)
      await hide.trigger('click')
      expect(prefs.sideHidden).toBe(true)
      expect(cols(w)).toMatch(/^0px /)
      expect(w.find('button.side-toggle').attributes('aria-label')).toBe('Show sidebar')
      expect(w.find('button.side-toggle').attributes('aria-expanded')).toBe('false')
      expect(w.find('[role="separator"]').exists()).toBe(false)
      w.unmount()
    })

    it('the separator resizes with the keyboard and resets on double click', async () => {
      const w = frame()
      const sep = w.find('[role="separator"]')
      expect(sep.attributes('aria-orientation')).toBe('vertical')
      expect(sep.attributes('aria-valuemin')).toBe('160')
      expect(sep.attributes('aria-valuemax')).toBe('420')
      expect(sep.attributes('aria-valuenow')).toBe('232')
      expect(sep.attributes('tabindex')).toBe('0')
      await sep.trigger('keydown', { key: 'ArrowRight' })
      expect(prefs.sideWidth).toBe(248)
      await sep.trigger('keydown', { key: 'ArrowLeft' })
      await sep.trigger('keydown', { key: 'ArrowLeft' })
      expect(prefs.sideWidth).toBe(216)
      await sep.trigger('keydown', { key: 'End' })
      expect(prefs.sideWidth).toBe(420)
      await sep.trigger('keydown', { key: 'Home' })
      expect(prefs.sideWidth).toBe(160)
      await sep.trigger('dblclick')
      expect(prefs.sideWidth).toBe(232)
      w.unmount()
    })

    it('a pointer drag clamps the width and releases the pointer', async () => {
      const w = frame()
      const el = w.find('[role="separator"]').element as HTMLElement
      const captured = new Set<number>()
      el.setPointerCapture = (id: number) => void captured.add(id)
      el.releasePointerCapture = (id: number) => void captured.delete(id)
      el.hasPointerCapture = (id: number) => captured.has(id)
      const ev = (type: string, clientX: number, buttons = type === 'pointerup' ? 0 : 1) => {
        const e = new MouseEvent(type, { clientX, bubbles: true, button: 0, buttons })
        Object.defineProperty(e, 'pointerId', { value: 7 })
        el.dispatchEvent(e)
      }
      ev('pointerdown', 232)
      expect(captured.has(7)).toBe(true)
      ev('pointermove', 282)
      expect(prefs.sideWidth).toBe(282)
      ev('pointermove', 900)
      expect(prefs.sideWidth).toBe(420)
      ev('pointerup', 900)
      expect(captured.has(7)).toBe(false)
      ev('pointermove', 300)
      expect(prefs.sideWidth).toBe(420)
      w.unmount()
    })
  })

  describe('sidebar drag that loses the pointer (#78)', () => {
    function frame() {
      store.state.connection = 'connected'
      store.state.data = { platform: 'darwin', account: { cswap: false, known: false, default: '' }, workspaces: [] }
      return mount(App, { attachTo: document.body })
    }
    function fire(el: HTMLElement, type: string, clientX: number, buttons: number) {
      const e = new MouseEvent(type, { clientX, bubbles: true, button: 0, buttons })
      Object.defineProperty(e, 'pointerId', { value: 1 })
      el.dispatchEvent(e)
    }

    it('stops following the cursor after lostpointercapture', () => {
      const w = frame()
      const el = w.find('[role="separator"]').element as HTMLElement
      fire(el, 'pointerdown', 232, 1)
      fire(el, 'pointermove', 260, 1)
      expect(prefs.sideWidth).toBe(260)
      fire(el, 'lostpointercapture', 260, 0)
      fire(el, 'pointermove', 380, 0)
      expect(prefs.sideWidth).toBe(260)
      w.unmount()
    })

    it('ignores a move with no button held, even mid-drag', () => {
      const w = frame()
      const el = w.find('[role="separator"]').element as HTMLElement
      fire(el, 'pointerdown', 232, 1)
      fire(el, 'pointermove', 300, 0)
      expect(prefs.sideWidth).toBe(232)
      w.unmount()
    })
  })

  describe('Messages drawer opens for approvals', () => {
    function folder(name: string, awaiting: number) {
      return { name, path: `/x/${name}`, permission_mode: 'auto', status: 'idle' as const, on_default_login: false, pending: 0, awaiting_approval: awaiting }
    }
    function data(a: number, b: number) {
      return {
        platform: 'darwin',
        account: { cswap: false, known: false, default: '' },
        workspaces: [
          { name: 'one', folders: [folder('x', a)] },
          { name: 'two', folders: [folder('y', b)] },
        ],
      }
    }

    it('opens when the active workspace gets a message that awaits approval', async () => {
      store.state.data = data(0, 0)
      store.ui.workspace = 'one'
      store.ui.messagesOpen = false
      const w = mount(App)
      store.state.data = data(1, 0)
      await nextTick()
      expect(store.ui.messagesOpen).toBe(true)
      w.unmount()
      store.ui.workspace = null
    })

    it('does not reopen when the user switches back to a workspace with approvals (#79)', async () => {
      store.state.data = data(3, 0)
      store.ui.workspace = 'one'
      store.ui.messagesOpen = false
      const w = mount(App)
      store.ui.workspace = 'two'
      await nextTick()
      store.ui.workspace = 'one'
      await nextTick()
      expect(store.ui.messagesOpen).toBe(false)
      w.unmount()
      store.ui.workspace = null
    })

    it('keeps an automatic open out of the saved choice, but saves a click (#79)', async () => {
      store.state.data = data(0, 0)
      store.ui.workspace = 'one'
      store.ui.messagesOpen = false
      const w = mount(App)
      store.state.data = data(1, 0)
      await nextTick()
      expect(store.ui.messagesOpen).toBe(true)
      expect(prefs.messagesOpen).toBe(false)
      await w.find('button.msg-toggle').trigger('click')
      expect(store.ui.messagesOpen).toBe(false)
      expect(prefs.messagesOpen).toBe(false)
      await w.find('button.msg-toggle').trigger('click')
      expect(prefs.messagesOpen).toBe(true)
      w.unmount()
      store.ui.workspace = null
    })

    it('stays closed when another workspace gets one', async () => {
      store.state.data = data(0, 0)
      store.ui.workspace = 'one'
      store.ui.messagesOpen = false
      const w = mount(App)
      store.state.data = data(0, 2)
      await nextTick()
      expect(store.ui.messagesOpen).toBe(false)
      w.unmount()
      store.ui.workspace = null
    })
  })
})
