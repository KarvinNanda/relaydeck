import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { ApiError } from '../api'
import { createMessages, type MessagesDeps } from '../stores/messages'
import type { DaemonEvent, Message, MessageState } from '../types'
import Timeline from './Timeline.vue'

// 10:42:05 local time, whatever the machine's time zone is.
const created = new Date(2026, 10, 17, 10, 42, 5).toISOString()

function msg(id: string, state: MessageState, extra: Partial<Message> = {}): Message {
  return { id, workspace: 'w', from: 'be', to: 'fe-admin', body: 'API is up on :8080', hop: 1, created, state, ...extra }
}

async function setup(
  list: Message[],
  post: (path: string, body?: unknown) => Promise<unknown> = async () => undefined,
  get: () => Promise<unknown> = () => Promise.resolve({ messages: list }),
) {
  const listeners = new Set<(e: DaemonEvent) => void>()
  const api = vi.fn((method: string, path: string, body?: unknown) =>
    method === 'GET' ? get() : post(path, body),
  )
  const store = {
    state: reactive({ connection: 'connected' }),
    activeWorkspace: () => ({ name: 'w', folders: [] }),
    onEvent: (fn: (e: DaemonEvent) => void) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
  }
  const m = createMessages({ api, store } as unknown as MessagesDeps)
  m.start()
  await flushPromises()
  const w = mount(Timeline, { props: { messages: m } })
  const emit = (x: Message) => {
    for (const fn of listeners) fn({ type: 'message', ws: 'w', folder: x.to, message: x })
  }
  return { w, api, m, emit }
}

const rows = (w: ReturnType<typeof mount>) => w.findAll('[role="row"]:not(.th)')

describe('Timeline', () => {
  it('renders every message state with its label and an icon', async () => {
    const labels: Record<MessageState, string> = {
      queued: 'queued',
      held_draft: 'waiting (draft)',
      held_external: 'held: session open in terminal',
      nudged: 'nudged',
      delivered: 'delivered (read)',
      awaiting_approval: 'awaiting approval (hop 3)',
      rejected: 'rejected',
    }
    const states = Object.keys(labels) as MessageState[]
    const { w } = await setup(states.map((s, i) => msg(String(i), s, { hop: s === 'awaiting_approval' ? 3 : 1 })))
    const got = rows(w).map((r) => r.find('.st').text())
    // newest first: the last message is the first row
    expect(got).toEqual([...states].reverse().map((s) => labels[s]))
    for (const r of rows(w)) expect(r.find('.st svg').exists()).toBe(true)
  })

  it('shows the time as local HH:MM:SS and the route as from -> to', async () => {
    const { w } = await setup([msg('1', 'queued')])
    const r = rows(w)[0]
    expect(r.find('.time').text()).toBe('10:42:05')
    expect(r.find('.route').text()).toBe('be -> fe-admin')
  })

  it('newest first', async () => {
    const { w } = await setup([msg('1', 'queued', { body: 'first' }), msg('2', 'queued', { body: 'second' })])
    expect(rows(w).map((r) => r.find('.cell-msg .body').text())).toEqual(['second', 'first'])
  })

  it('Approve and Reject call their endpoints', async () => {
    const { w, api } = await setup([msg('m1', 'awaiting_approval', { hop: 3 })])
    const btns = rows(w)[0].findAll('button.act')
    expect(btns.map((b) => b.text())).toEqual(['Approve', 'Reject'])
    await btns[0].trigger('click')
    await flushPromises()
    expect(api).toHaveBeenCalledWith('POST', '/api/messages/m1/approve')
    await btns[1].trigger('click')
    await flushPromises()
    expect(api).toHaveBeenCalledWith('POST', '/api/messages/m1/reject')
  })

  describe('action errors', () => {
    let warn: ReturnType<typeof vi.spyOn>
    beforeEach(() => {
      warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    })
    afterEach(() => warn.mockRestore())

    it('shows plain words inline on its row, keeps the server text out of the UI, and disables the buttons while busy', async () => {
      let fail!: (e: unknown) => void
      const { w } = await setup([msg('m1', 'awaiting_approval'), msg('m2', 'awaiting_approval')], () => new Promise((_, rej) => (fail = rej)))
      const [first, second] = rows(w)
      await second.find('button.act').trigger('click')
      expect(second.findAll('button.act').every((b) => b.attributes('disabled') !== undefined)).toBe(true)
      const raw = 'message "m1" is delivered, not awaiting_approval'
      fail(new ApiError(409, raw))
      await flushPromises()
      const alert = second.find('[role="alert"]')
      expect(alert.text()).toBe('Already handled, nothing to approve.')
      expect(w.text()).not.toContain(raw)
      expect(warn).toHaveBeenCalledWith('relaydeck: approve failed', 409, raw)
      // the buttons point at the error
      expect(second.find('button.act').attributes('aria-describedby')).toBe(alert.attributes('id'))
      expect(first.find('[role="alert"]').exists()).toBe(false)
      expect(second.find('button.act').attributes('disabled')).toBeUndefined()
    })

    it.each([
      ['awaiting_approval', 0, 409, 'Already handled, nothing to approve.'],
      ['awaiting_approval', 1, 409, 'Already handled, nothing to reject.'],
      ['awaiting_approval', 0, 404, 'This message no longer exists.'],
      ['awaiting_approval', 1, 500, 'Could not reject. See the relaydeck log.'],
      ['held_external', 0, 409, 'This folder already has a session running. Stop it first, then try again.'],
      ['held_external', 0, 404, 'This folder is no longer in the workspace.'],
      ['held_external', 0, 503, 'Could not start a new session. See the relaydeck log.'],
      ['held_draft', 0, 404, 'This folder is no longer in the workspace.'],
      ['held_draft', 0, 500, 'Could not deliver now. See the relaydeck log.'],
    ] as const)('%s button %i, HTTP %i: "%s"', async (state, button, status, text) => {
      const { w } = await setup([msg('m1', state)], () => Promise.reject(new ApiError(status, 'raw server text')))
      await rows(w)[0].findAll('button.act')[button].trigger('click')
      await flushPromises()
      expect(rows(w)[0].find('[role="alert"]').text()).toBe(text)
    })

    it('says relaydeck is not reachable when the request never arrived', async () => {
      const { w } = await setup([msg('m1', 'held_draft')], () => Promise.reject(new ApiError(0, 'relaydeck is not reachable. Is `relaydeck serve` still running?')))
      await rows(w)[0].find('button.act').trigger('click')
      await flushPromises()
      expect(rows(w)[0].find('[role="alert"]').text()).toBe('relaydeck is not reachable. Is `relaydeck serve` still running?')
    })

    it('clears the error when the row changes state', async () => {
      const { w, emit } = await setup([msg('m1', 'held_draft')], () => Promise.reject(new ApiError(500, 'x')))
      await rows(w)[0].find('button.act').trigger('click')
      await flushPromises()
      expect(rows(w)[0].find('[role="alert"]').exists()).toBe(true)
      emit(msg('m1', 'nudged'))
      await flushPromises()
      expect(rows(w)[0].find('[role="alert"]').exists()).toBe(false)
      // back in the same state later: the old error does not come back
      emit(msg('m1', 'held_draft'))
      await flushPromises()
      expect(rows(w)[0].find('[role="alert"]').exists()).toBe(false)
    })

    it('shows the error only while the row still has actions', async () => {
      const { w } = await setup([msg('m1', 'held_external')], () => Promise.reject(new ApiError(409, 'x')))
      const r = rows(w)[0]
      await r.findAll('button.act')[0].trigger('click')
      await flushPromises()
      expect(r.find('[role="alert"]').exists()).toBe(true)
      await r.findAll('button.act')[1].trigger('click') // Wait
      expect(r.find('[role="alert"]').exists()).toBe(false)
    })
  })

  it('held_external: Start new session posts new: true, Wait hides the actions', async () => {
    const { w, api } = await setup([msg('m1', 'held_external', { to: 'worker' })])
    const r = rows(w)[0]
    expect(r.findAll('button.act').map((b) => b.text())).toEqual(['Start new session', 'Wait'])
    await r.findAll('button.act')[0].trigger('click')
    await flushPromises()
    expect(api).toHaveBeenCalledWith('POST', '/api/agents/w/worker/start', { new: true })
    await r.findAll('button.act')[1].trigger('click')
    expect(r.findAll('button.act')).toHaveLength(0)
    expect(r.find('.st').text()).toBe('held: session open in terminal')
  })

  it('held_draft: Deliver now posts deliver-now', async () => {
    const { w, api } = await setup([msg('m1', 'held_draft')])
    const b = rows(w)[0].find('button.act')
    expect(b.text()).toBe('Deliver now')
    await b.trigger('click')
    await flushPromises()
    expect(api).toHaveBeenCalledWith('POST', '/api/agents/w/fe-admin/deliver-now')
  })

  it('names the real hop of a message held past the limit', async () => {
    const { w } = await setup([msg('m1', 'awaiting_approval', { hop: 5 })])
    expect(rows(w)[0].find('.st').text()).toBe('awaiting approval (hop 5)')
  })

  it('other states have no actions', async () => {
    const { w } = await setup(['queued', 'nudged', 'delivered', 'rejected'].map((s, i) => msg(String(i), s as MessageState)))
    expect(w.findAll('button.act')).toHaveLength(0)
  })

  it('shows the first 120 characters of a long message and expands to the full text', async () => {
    const body = 'x'.repeat(119) + 'YZ' + ' tail'.repeat(40)
    const { w } = await setup([msg('m1', 'queued', { body })])
    const cell = rows(w)[0].find('.cell-msg')
    const b = cell.find('button.exp')
    expect(b.attributes('aria-label')).toBe('Full message from be to fe-admin')
    expect(b.attributes('aria-expanded')).toBe('false')
    // the button holds only the chevron; the text is outside it
    expect(b.text()).toBe('')
    expect(b.find('.body').exists()).toBe(false)
    expect(cell.find('.body').text()).toBe('x'.repeat(119) + 'Y…')
    await b.trigger('click')
    expect(b.attributes('aria-expanded')).toBe('true')
    expect(cell.find('.body').text()).toBe(body)
    await b.trigger('click')
    expect(cell.find('.body').text()).toBe('x'.repeat(119) + 'Y…')
  })

  it('a click on the message text also expands it', async () => {
    const { w } = await setup([msg('m1', 'queued', { body: 'x'.repeat(200) })])
    const cell = rows(w)[0].find('.cell-msg')
    await cell.find('.body').trigger('click')
    expect(cell.find('button.exp').attributes('aria-expanded')).toBe('true')
  })

  it('keeps a short message whole and shows line breaks as spaces until expanded', async () => {
    const { w } = await setup([msg('m1', 'queued', { body: 'line one\nline two' })])
    const cell = rows(w)[0].find('.cell-msg')
    expect(cell.find('.body').text()).toBe('line one line two')
    await cell.find('button.exp').trigger('click')
    expect(cell.find('.body').element.textContent).toBe('line one\nline two')
  })

  it('renders agent text as text, never as HTML', async () => {
    const { w } = await setup([msg('m1', 'queued', { body: '<img src=x onerror=alert(1)>', from: '<b>x</b>' })])
    expect(w.find('img').exists()).toBe(false)
    expect(w.find('b').exists()).toBe(false)
    expect(rows(w)[0].find('.cell-msg .body').text()).toBe('<img src=x onerror=alert(1)>')
  })

  it('marks a row that just changed', async () => {
    const { w, emit } = await setup([msg('m1', 'queued')])
    expect(rows(w)[0].classes()).not.toContain('hot')
    emit(msg('m1', 'delivered'))
    await flushPromises()
    expect(rows(w)[0].classes()).toContain('hot')
  })

  it('says so when there are no messages, outside the table', async () => {
    const { w } = await setup([])
    expect(rows(w)).toHaveLength(0)
    const p = w.find('p.empty')
    expect(p.text()).toBe('No messages yet.')
    expect(p.element.closest('[role="table"]')).toBeNull()
    // no header row without rows: the column widths come from the rows
    expect(w.find('[role="columnheader"]').exists()).toBe(false)
  })

  it('has only rows inside the table', async () => {
    const { w } = await setup([msg('m1', 'queued')])
    const kids = [...w.find('[role="table"]').element.children]
    expect(kids.length).toBe(2)
    for (const c of kids) expect(c.getAttribute('role')).toBe('row')
  })

  it('shows a load error in plain words outside the table, with Retry', async () => {
    const { w, api } = await setup([], undefined, () => Promise.reject(new ApiError(500, 'internal error; see the relaydeck log')))
    const alert = w.find('[role="alert"]')
    expect(alert.text()).toContain('Could not load the messages.')
    expect(alert.element.closest('[role="table"]')).toBeNull()
    api.mockClear()
    await alert.find('button').trigger('click')
    expect(api).toHaveBeenCalledWith('GET', '/api/workspaces/w/messages')
  })
})
