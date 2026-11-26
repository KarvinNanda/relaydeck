import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import Sidebar from './Sidebar.vue'
import AddFolderDialog from './AddFolderDialog.vue'
import type { FolderState, State } from '../types'

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>()
  return { ...real, api: vi.fn() }
})
const apiMock = vi.mocked(api)

function folder(name: string, over: Partial<FolderState> = {}): FolderState {
  return {
    name,
    path: `/x/${name}`,
    permission_mode: 'auto',
    status: 'stopped',
    on_default_login: false,
    pending: 0,
    awaiting_approval: 0,
    ...over,
  }
}

function snapshot(platform = 'darwin'): State {
  return {
    platform,
    account: { cswap: true, known: true, default: '1' },
    workspaces: [
      {
        name: 'game-lounge',
        folders: [
          folder('be', { status: 'busy', account: 'work-2' }),
          folder('fe-admin', { status: 'idle', account: 'work-2', pending: 1 }),
          folder('fe-customer', { status: 'attention', account: 'personal-1', on_default_login: true }),
          folder('worker', { pending: 2, awaiting_approval: 3 }),
        ],
      },
      { name: 'infra-tools', folders: [folder('tf'), folder('ci', { status: 'attention' })] },
    ],
  }
}

function closeAll(): void {
  for (const p of [...panelStore.panels]) panelStore.closePanel(p.ws, p.folder)
}

function row(w: ReturnType<typeof mount>, name: string) {
  const r = w.findAll('li.node').find((li) => li.find('.node-name').text() === name)
  if (!r) throw new Error(`no row ${name}`)
  return r
}

beforeEach(() => {
  closeAll()
  store.state.data = snapshot()
  store.ui.workspace = null
  apiMock.mockReset()
  apiMock.mockImplementation(async (method: string, path: string) => {
    if (method === 'GET' && path === '/api/state') return snapshot()
    throw new Error(`unexpected ${method} ${path}`)
  })
})

afterEach(() => {
  store.state.data = null
  store.ui.workspace = null
  closeAll()
  document.body.innerHTML = ''
})

describe('Sidebar', () => {
  it('lists workspaces with a folder count', () => {
    const w = mount(Sidebar)
    const heads = w.findAll('button.ws-row')
    expect(heads.map((h) => h.find('.name').text())).toEqual(['game-lounge', 'infra-tools'])
    expect(heads[0].find('.meta').text()).toContain('4 folders')
    expect(heads[1].find('.meta').text()).toContain('2 folders')
  })

  it('says 1 folder, singular', () => {
    store.state.data = { ...snapshot(), workspaces: [{ name: 'solo', folders: [folder('a')] }] }
    const w = mount(Sidebar)
    expect(w.find('button.ws-row .meta').text()).toContain('1 folder')
    expect(w.find('button.ws-row .meta').text()).not.toContain('folders')
  })

  it('collapses and expands a workspace', async () => {
    const w = mount(Sidebar)
    const head = w.findAll('button.ws-row')[0]
    expect(head.attributes('aria-expanded')).toBe('true')
    expect(w.text()).toContain('fe-admin')
    await head.trigger('click')
    expect(head.attributes('aria-expanded')).toBe('false')
    expect(w.text()).not.toContain('fe-admin')
    await head.trigger('click')
    expect(w.text()).toContain('fe-admin')
  })

  it('a collapsed workspace shows the folder count and how many need attention', async () => {
    const w = mount(Sidebar)
    const head = w.findAll('button.ws-row')[1]
    await head.trigger('click')
    expect(head.attributes('aria-expanded')).toBe('false')
    expect(head.find('.meta').text()).toBe('2 folders · 1 needs attention')
  })

  it('says "need attention" for more than one folder', async () => {
    const s = snapshot()
    s.workspaces[1].folders[0].status = 'attention'
    store.state.data = s
    const w = mount(Sidebar)
    const head = w.findAll('button.ws-row')[1]
    await head.trigger('click')
    expect(head.find('.meta').text()).toBe('2 folders · 2 need attention')
  })

  it('a collapsed workspace with no folder needing attention shows only the count', async () => {
    const w = mount(Sidebar)
    const head = w.findAll('button.ws-row')[0]
    store.state.data!.workspaces[0].folders[2].status = 'idle'
    await head.trigger('click')
    expect(head.find('.meta').text()).toBe('4 folders')
  })

  it('shows each folder with status icon, name, status word and account', () => {
    const w = mount(Sidebar)
    const be = row(w, 'be')
    expect(be.find('.node-name svg.i').classes()).toContain('c-busy')
    expect(be.find('.node-meta .st').text()).toBe('busy')
    expect(be.find('.node-meta .st').classes()).toContain('c-busy')
    // The "·" before the account is drawn by CSS, so a wrapped account
    // starts its own line without it (mockup A, personal-1).
    expect(be.find('.node-meta .acct').text()).toBe('work-2')
    expect(row(w, 'fe-customer').find('.node-meta .st').text()).toBe('needs attention')
    expect(row(w, 'fe-customer').find('.node-meta .acct').text()).toBe('personal-1')
    expect(row(w, 'worker').find('.node-meta .acct').text()).toBe('no account')
    expect(w.find('.node-meta').text()).not.toContain('·')
  })

  it('never shows the account on windows', () => {
    const s = snapshot('windows')
    s.workspaces[0].folders[0].account = 'work-2' // even if the server sent one
    store.state.data = s
    const w = mount(Sidebar)
    expect(row(w, 'be').find('.node-meta').text()).toBe('busy')
    expect(row(w, 'worker').find('.node-meta').text()).toBe('stopped')
    expect(w.find('.node-meta .acct').exists()).toBe(false)
    expect(w.text()).not.toContain('no account')
    expect(w.text()).not.toContain('work-2')
  })

  it('never shows the default login note on windows', () => {
    const s = snapshot('windows')
    s.workspaces[0].folders[2].on_default_login = true // even if the server sent it
    store.state.data = s
    const w = mount(Sidebar)
    expect(w.find('.note').exists()).toBe(false)
    expect(w.text()).not.toContain('On default login')
  })

  it('shows the default login note only when on_default_login is set', () => {
    const w = mount(Sidebar)
    expect(row(w, 'fe-customer').find('.note').text()).toBe('On default login: a cswap switch will move it')
    expect(row(w, 'be').find('.note').exists()).toBe(false)
    expect(w.findAll('.note')).toHaveLength(1)
  })

  it('shows chips for waiting messages and messages awaiting approval', () => {
    const w = mount(Sidebar)
    expect(row(w, 'fe-admin').find('.chip').text()).toBe('1 message waiting')
    const chips = row(w, 'worker').findAll('.chip').map((c) => c.text())
    expect(chips).toEqual(['2 messages waiting', '3 awaiting approval'])
    expect(row(w, 'be').find('.chip').exists()).toBe(false)
  })

  it('clicking a folder opens its panel, selects it and makes its workspace active', async () => {
    const w = mount(Sidebar)
    await row(w, 'tf').find('button.node-btn').trigger('click')
    expect(panelStore.panels).toEqual([{ ws: 'infra-tools', folder: 'tf' }])
    expect(store.ui.workspace).toBe('infra-tools')
    expect(row(w, 'tf').find('button.node-btn').attributes('aria-current')).toBe('true')
    expect(row(w, 'be').find('button.node-btn').attributes('aria-current')).toBeUndefined()
  })

  it('shows the refusal reason in a dismissible alert when 4 panels are open', async () => {
    const w = mount(Sidebar)
    for (const n of ['be', 'fe-admin', 'fe-customer', 'worker']) await row(w, n).find('button.node-btn').trigger('click')
    expect(w.find('[role="alert"]').exists()).toBe(false)
    await row(w, 'tf').find('button.node-btn').trigger('click')
    const alert = w.find('[role="alert"]')
    expect(alert.text()).toContain('At most 4 panels can be open. Close one first.')
    await alert.find('button[aria-label="Dismiss"]').trigger('click')
    expect(w.find('[role="alert"]').exists()).toBe(false)
    // a later successful open clears an old alert as well
    await row(w, 'tf').find('button.node-btn').trigger('click')
    panelStore.closePanel('game-lounge', 'be')
    await row(w, 'tf').find('button.node-btn').trigger('click')
    expect(w.find('[role="alert"]').exists()).toBe(false)
  })

  it('Add folder opens the dialog for that workspace', async () => {
    const w = mount(Sidebar)
    expect(w.findComponent(AddFolderDialog).exists()).toBe(false)
    await w.findAll('button.add-folder')[1].trigger('click')
    const dlg = w.findComponent(AddFolderDialog)
    expect(dlg.exists()).toBe(true)
    expect(dlg.props('ws')).toBe('infra-tools')
    dlg.vm.$emit('close')
    await flushPromises()
    expect(w.findComponent(AddFolderDialog).exists()).toBe(false)
  })

  it('Add workspace checks the name in the client', async () => {
    const w = mount(Sidebar)
    await w.find('button.add-ws').trigger('click')
    expect(w.find('input#new-ws').attributes('placeholder')).toBe('e.g. game-lounge')
    await w.find('input#new-ws').setValue('Bad Name')
    await w.find('form.ws-form').trigger('submit')
    await flushPromises()
    expect(apiMock).not.toHaveBeenCalled()
    expect(w.find('input#new-ws').attributes('aria-invalid')).toBe('true')
    expect(w.find('form.ws-form').text()).toContain('lowercase')
  })

  it('Add workspace posts the name and refreshes the state', async () => {
    apiMock.mockImplementation(async (method: string, path: string) => {
      if (method === 'POST' && path === '/api/workspaces') return { name: 'tools' }
      return snapshot()
    })
    const w = mount(Sidebar)
    await w.find('button.add-ws').trigger('click')
    await w.find('input#new-ws').setValue('tools')
    await w.find('form.ws-form').trigger('submit')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/workspaces', { name: 'tools' })
    expect(apiMock).toHaveBeenCalledWith('GET', '/api/state')
    expect(w.find('form.ws-form').exists()).toBe(false)
  })

  it('Add workspace shows the server error text', async () => {
    apiMock.mockImplementation(async () => {
      throw new ApiError(409, 'workspace "tools" already exists')
    })
    const w = mount(Sidebar)
    await w.find('button.add-ws').trigger('click')
    await w.find('input#new-ws').setValue('tools')
    await w.find('form.ws-form').trigger('submit')
    await flushPromises()
    expect(w.find('form.ws-form [role="alert"]').text()).toContain('workspace "tools" already exists')
  })

  describe('remove (#116)', () => {
    function wsBlock(w: ReturnType<typeof mount>, name: string) {
      const b = w.findAll('div.ws').find((d) => d.find('.ws-row .name').text() === name)
      if (!b) throw new Error(`no workspace ${name}`)
      return b
    }

    it('every folder row has a labelled remove button next to it, always visible', () => {
      const w = mount(Sidebar)
      const b = row(w, 'tf').find('button.rm')
      expect(b.attributes('aria-label')).toBe('Remove folder tf')
      expect(b.attributes('title')).toBe('Remove folder tf')
    })

    it('asks first, says nothing on disk is deleted, then deletes and refreshes', async () => {
      apiMock.mockImplementation(async () => snapshot())
      const w = mount(Sidebar, { attachTo: document.body })
      await row(w, 'tf').find('button.rm').trigger('click')
      const box = row(w, 'tf').find('.rm-confirm')
      expect(box.text()).toContain('Remove tf from relaydeck?')
      expect(box.text()).toContain('The folder and its Claude Code sessions stay on disk.')
      expect(document.activeElement?.textContent).toContain('Cancel') // the safe choice has focus
      expect(apiMock).not.toHaveBeenCalled()
      await box.find('button.btn-danger').trigger('click')
      await flushPromises()
      expect(apiMock).toHaveBeenCalledWith('DELETE', '/api/workspaces/infra-tools/folders/tf')
      expect(apiMock).toHaveBeenCalledWith('GET', '/api/state')
      expect(w.find('.rm-confirm').exists()).toBe(false)
      w.unmount()
    })

    it('Cancel and Escape close the question without a request', async () => {
      const w = mount(Sidebar)
      await row(w, 'tf').find('button.rm').trigger('click')
      await row(w, 'tf').find('.rm-confirm button:not(.btn-danger)').trigger('click')
      expect(w.find('.rm-confirm').exists()).toBe(false)
      await row(w, 'tf').find('button.rm').trigger('click')
      await row(w, 'tf').find('.rm-confirm').trigger('keydown', { key: 'Escape' })
      expect(w.find('.rm-confirm').exists()).toBe(false)
      expect(apiMock).not.toHaveBeenCalled()
    })

    it('a folder that is not stopped says to stop it first, with no remove button', async () => {
      const w = mount(Sidebar)
      await row(w, 'be').find('button.rm').trigger('click')
      const box = row(w, 'be').find('.rm-confirm')
      expect(box.text()).toContain('Stop be before you remove it.')
      expect(box.find('button.btn-danger').exists()).toBe(false)
    })

    it('shows the server error in the question', async () => {
      apiMock.mockImplementation(async () => {
        throw new ApiError(409, 'a running agent cannot be removed: stop tf first')
      })
      const w = mount(Sidebar)
      await row(w, 'tf').find('button.rm').trigger('click')
      await row(w, 'tf').find('.rm-confirm button.btn-danger').trigger('click')
      await flushPromises()
      expect(row(w, 'tf').find('.rm-confirm [role="alert"]').text()).toContain('stop tf first')
    })

    it('one question at a time: opening another closes the first', async () => {
      const w = mount(Sidebar)
      await row(w, 'tf').find('button.rm').trigger('click')
      await row(w, 'worker').find('button.rm').trigger('click')
      expect(w.findAll('.rm-confirm').length).toBe(1)
      expect(row(w, 'worker').find('.rm-confirm').exists()).toBe(true)
    })

    it('Remove workspace asks with the folder count and deletes it', async () => {
      const snap = snapshot()
      snap.workspaces[1].folders[1].status = 'stopped'
      store.state.data = snap
      apiMock.mockImplementation(async () => snap)
      const w = mount(Sidebar)
      await wsBlock(w, 'infra-tools').find('button.rm-ws').trigger('click')
      const box = wsBlock(w, 'infra-tools').find('.rm-confirm')
      expect(box.text()).toContain('Remove workspace infra-tools and its 2 folders from relaydeck?')
      expect(box.text()).toContain('The folders and their Claude Code sessions stay on disk.')
      await box.find('button.btn-danger').trigger('click')
      await flushPromises()
      expect(apiMock).toHaveBeenCalledWith('DELETE', '/api/workspaces/infra-tools')
    })

    it('Remove workspace names the folders to stop first', async () => {
      const w = mount(Sidebar)
      await wsBlock(w, 'game-lounge').find('button.rm-ws').trigger('click')
      const box = wsBlock(w, 'game-lounge').find('.rm-confirm')
      expect(box.text()).toContain('Stop be, fe-admin, fe-customer before you remove this workspace.')
      expect(box.find('button.btn-danger').exists()).toBe(false)
    })
  })

  it('shows the status key with all five statuses', () => {
    const w = mount(Sidebar)
    // no heading row: the group is named for screen readers instead
    expect(w.find('.legend-title').exists()).toBe(false)
    expect(w.find('.legend').attributes('aria-label')).toBe('Status key')
    expect(w.findAll('.legend span').map((s) => s.text())).toEqual([
      'busy',
      'idle',
      'starting',
      'stopped',
      'needs attention',
    ])
  })

  it('puts Add workspace right after the workspace list, and keeps only the status key in the footer (#96)', () => {
    store.state.data = snapshot()
    const w = mount(Sidebar)
    const tree = w.find('.sb-tree')
    const add = tree.find('.ws-add button.add-ws')
    expect(add.exists()).toBe(true)
    // last child of the scroll box, so it follows the list and can stick to its bottom
    expect(tree.element.lastElementChild).toBe(w.find('.ws-add').element)
    expect(w.find('.side-foot .add-ws').exists()).toBe(false)
    expect(w.find('.side-foot .legend').exists()).toBe(true)
  })

  it('says so when there are no workspaces', () => {
    store.state.data = { ...snapshot(), workspaces: [] }
    const w = mount(Sidebar)
    expect(w.text()).toContain('No workspaces yet')
    expect(w.find('button.add-ws').exists()).toBe(true)
  })

  it('shows every folder name in full, never shortened', () => {
    store.state.data = snapshot()
    const w = mount(Sidebar)
    const names = w.findAll('.node-name').map((n) => n.text())
    expect(names).toEqual(expect.arrayContaining(['be', 'fe-admin', 'fe-customer', 'worker']))
    expect(w.findAll('.node-btn').map((b) => b.attributes('title'))).toContain('/x/fe-customer')
  })
})
