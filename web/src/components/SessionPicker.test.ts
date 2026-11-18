import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import SessionPicker, { relTime } from './SessionPicker.vue'
import type { Session } from '../types'

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>()
  return { ...real, api: vi.fn() }
})
const apiMock = vi.mocked(api)

const now = new Date('2026-11-18T10:00:00Z')
const ids = {
  a: '7f3a9c10-0000-4000-8000-000000000001',
  b: '41be0d20-0000-4000-8000-000000000002',
  c: 'c9d27e80-0000-4000-8000-000000000003',
}
// Not sorted on purpose: the picker sorts newest first.
const list: Session[] = [
  { id: ids.c, modified: '2026-11-16T10:00:00Z', title: '' },
  { id: ids.a, modified: '2026-11-18T09:58:00Z', title: 'Fix login redirect loop' },
  { id: ids.b, modified: '2026-11-17T08:00:00Z', title: 'Add admin audit log page' },
]

function mountPicker() {
  return mount(SessionPicker, { props: { ws: 'game-lounge', folder: 'fe-admin' }, attachTo: document.body })
}

function closeAll(): void {
  for (const p of [...panelStore.panels]) panelStore.closePanel(p.ws, p.folder)
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(now)
  closeAll()
  store.state.data = {
    platform: 'darwin',
    account: { cswap: false, known: false, default: '' },
    workspaces: [
      {
        name: 'game-lounge',
        folders: [
          {
            name: 'fe-admin',
            path: '/x/fe-admin',
            permission_mode: 'auto',
            session_id: ids.a,
            status: 'stopped',
            on_default_login: false,
            pending: 0,
            awaiting_approval: 0,
          },
        ],
      },
    ],
  }
  apiMock.mockReset()
  apiMock.mockImplementation(async (method: string, path: string) => {
    if (method === 'GET' && path === '/api/workspaces/game-lounge/folders/fe-admin/sessions') return { sessions: list }
    if (method === 'POST' && path === '/api/agents/game-lounge/fe-admin/start') return undefined
    throw new Error(`unexpected ${method} ${path}`)
  })
})

afterEach(() => {
  vi.useRealTimers()
  store.state.data = null
  closeAll()
  document.body.innerHTML = ''
})

describe('relTime', () => {
  it('says how long ago, in plain words', () => {
    expect(relTime('2026-11-18T09:59:40Z', now)).toBe('just now')
    expect(relTime('2026-11-18T09:58:00Z', now)).toBe('2 min ago')
    expect(relTime('2026-11-18T07:00:00Z', now)).toBe('3 h ago')
    expect(relTime('2026-11-17T08:00:00Z', now)).toBe('yesterday')
    expect(relTime('2026-11-15T08:00:00Z', now)).toBe('3 days ago')
    expect(relTime('2026-10-01T08:00:00Z', now)).toBe('2026-10-01')
    expect(relTime('not a date', now)).toBe('')
  })
})

describe('SessionPicker', () => {
  it('lists sessions newest first and marks the current one', async () => {
    const w = mountPicker()
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('GET', '/api/workspaces/game-lounge/folders/fe-admin/sessions')
    expect(w.find('[role="dialog"]').attributes('aria-label')).toBe('Sessions')
    const rows = w.findAll('li.sess')
    expect(rows.map((r) => r.find('.t').text())).toEqual([
      'Fix login redirect loop',
      'Add admin audit log page',
      '(no title)',
    ])
    expect(rows[0].classes()).toContain('cur')
    expect(rows[0].text()).toContain('current')
    expect(rows[0].text()).toContain('2 min ago')
    expect(rows[0].text()).toContain('7f3a9c10')
    expect(rows[1].classes()).not.toContain('cur')
    expect(rows[1].text()).not.toContain('current')
    expect(rows[1].text()).toContain('yesterday')
    w.unmount()
  })

  it('picking a session starts it with session_id, opens the panel and closes', async () => {
    const w = mountPicker()
    await flushPromises()
    await w.findAll('li.sess button')[1].trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/start', { session_id: ids.b })
    expect(panelStore.panels).toEqual([{ ws: 'game-lounge', folder: 'fe-admin' }])
    expect(w.emitted('close')).toHaveLength(1)
    w.unmount()
  })

  it('New session starts with new: true', async () => {
    const w = mountPicker()
    await flushPromises()
    await w.find('button.new').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/start', { new: true })
    expect(panelStore.panels).toHaveLength(1)
    expect(w.emitted('close')).toHaveLength(1)
    w.unmount()
  })

  it('shows a start error inline and stays open', async () => {
    apiMock.mockImplementation(async (method: string) => {
      if (method === 'GET') return { sessions: list }
      throw new ApiError(409, 'agent game-lounge/fe-admin is already running')
    })
    const w = mountPicker()
    await flushPromises()
    await w.find('button.new').trigger('click')
    await flushPromises()
    expect(w.find('[role="alert"]').text()).toContain('agent game-lounge/fe-admin is already running')
    expect(w.emitted('close')).toBeFalsy()
    expect(panelStore.panels).toHaveLength(0)
    w.unmount()
  })

  it('refuses before starting when 4 other panels are open', async () => {
    for (const f of ['a', 'b', 'c', 'd']) panelStore.openPanel('other', f)
    const w = mountPicker()
    await flushPromises()
    await w.findAll('li.sess button')[0].trigger('click')
    await w.find('button.new').trigger('click')
    await flushPromises()
    // the agent is not started for a panel that cannot be shown
    expect(apiMock).not.toHaveBeenCalledWith('POST', expect.anything(), expect.anything())
    expect(w.find('[role="alert"]').text()).toContain('At most 4 terminals can be open. Close one first.')
    expect(w.emitted('close')).toBeFalsy()
    w.unmount()
  })

  it('starts when the grid is full but this folder already has a panel', async () => {
    for (const f of ['a', 'b', 'c']) panelStore.openPanel('other', f)
    panelStore.openPanel('game-lounge', 'fe-admin')
    const w = mountPicker()
    await flushPromises()
    await w.find('button.new').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/start', { new: true })
    expect(panelStore.focused).toEqual({ ws: 'game-lounge', folder: 'fe-admin' })
    expect(w.emitted('close')).toHaveLength(1)
    w.unmount()
  })

  it('shows a list error inline', async () => {
    apiMock.mockImplementation(async () => {
      throw new ApiError(404, 'no folder game-lounge/fe-admin')
    })
    const w = mountPicker()
    await flushPromises()
    expect(w.find('[role="alert"]').text()).toContain('no folder game-lounge/fe-admin')
    w.unmount()
  })

  it('says so when the folder has no sessions', async () => {
    apiMock.mockImplementation(async () => ({ sessions: [] }))
    const w = mountPicker()
    await flushPromises()
    expect(w.text()).toContain('No sessions yet')
    w.unmount()
  })

  it('closes on Escape and on the close button', async () => {
    const w = mountPicker()
    await flushPromises()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(w.emitted('close')).toHaveLength(1)
    await w.find('button[aria-label="Close sessions"]').trigger('click')
    expect(w.emitted('close')).toHaveLength(2)
    w.unmount()
  })
})
