// The Messages timeline of the active workspace. It loads
// GET /api/workspaces/{ws}/messages and then applies `message` events from
// the state store. Listeners never see the snapshot frame of /ws/events, so
// events missed while the socket was down would be lost: the list is loaded
// again every time the connection goes back to connected.
//
// Each event also "lights" its message for 2 s; the timeline and the graph
// use that to show what just changed.
import { computed, effectScope, reactive, ref, shallowRef, watch, type EffectScope } from 'vue'
import { api, ApiError } from '../api'
import type { DaemonEvent, Message } from '../types'
import { store, type Store } from './state'

export interface MessagesDeps {
  api: typeof api
  store: Pick<Store, 'state' | 'activeWorkspace' | 'onEvent'>
}

export interface Counts {
  awaiting: number // awaiting_approval: needs Approve or Reject
  held: number // held_external: needs "Start new session" or Wait
  waiting: number // held_draft: waits for the user's draft, or "Deliver now"
}

// How long a message stays lit after an event about it.
export const litMs = 2000

export function createMessages(deps: MessagesDeps = { api, store }) {
  const list = shallowRef<Message[]>([]) // creation order, as the daemon keeps it
  const loading = ref(false)
  const error = ref('')
  const lit = reactive(new Set<string>())
  const litTimers = new Map<string, ReturnType<typeof setTimeout>>()

  let scope: EffectScope | null = null
  let unlisten: (() => void) | null = null
  let ws: string | null = null
  let loadSeq = 0
  // Events seen while a load is in flight. They are applied again on top of
  // the answer, which may have been taken before them.
  let buffered: Message[] | null = null

  const newestFirst = computed(() => [...list.value].reverse())

  const counts = computed<Counts>(() => {
    const c: Counts = { awaiting: 0, held: 0, waiting: 0 }
    for (const m of list.value) {
      if (m.state === 'awaiting_approval') c.awaiting++
      else if (m.state === 'held_external') c.held++
      else if (m.state === 'held_draft') c.waiting++
    }
    return c
  })

  function start(): void {
    if (scope) return
    scope = effectScope(true)
    scope.run(() => {
      watch(
        () => deps.store.activeWorkspace()?.name ?? null,
        (name) => {
          ws = name
          list.value = []
          clearLit()
          error.value = ''
          if (name) void load(name)
          else {
            loadSeq++
            loading.value = false
          }
        },
        { immediate: true },
      )
      watch(
        () => deps.store.state.connection,
        (now, before) => {
          if (now === 'connected' && before !== 'connected' && ws) void load(ws)
        },
      )
    })
    unlisten = deps.store.onEvent(onEvent)
  }

  function stop(): void {
    scope?.stop()
    scope = null
    unlisten?.()
    unlisten = null
    loadSeq++
    buffered = null
    clearLit()
  }

  async function load(name: string): Promise<void> {
    const mine = ++loadSeq
    buffered = []
    loading.value = true
    try {
      const res = await deps.api<{ messages: Message[] }>('GET', `/api/workspaces/${enc(name)}/messages`)
      if (mine !== loadSeq) return
      let next = res?.messages ?? []
      for (const m of buffered ?? []) next = upsert(next, m)
      list.value = next
      error.value = ''
    } catch (e) {
      if (mine !== loadSeq) return
      error.value = e instanceof ApiError ? e.message : 'could not load the messages'
    } finally {
      if (mine === loadSeq) {
        loading.value = false
        buffered = null
      }
    }
  }

  function onEvent(e: DaemonEvent): void {
    if (e.type !== 'message' || ws === null || e.message.workspace !== ws) return
    buffered?.push(e.message)
    list.value = upsert(list.value, e.message)
    light(e.message.id)
  }

  function light(id: string): void {
    clearTimeout(litTimers.get(id))
    lit.add(id)
    litTimers.set(
      id,
      setTimeout(() => {
        litTimers.delete(id)
        lit.delete(id)
      }, litMs),
    )
  }

  function clearLit(): void {
    for (const t of litTimers.values()) clearTimeout(t)
    litTimers.clear()
    lit.clear()
  }

  // Row actions. They throw ApiError; the row shows its text. The daemon
  // sends a message event for every change, so nothing is updated here.
  const approve = (id: string) => deps.api<void>('POST', `/api/messages/${enc(id)}/approve`)
  const reject = (id: string) => deps.api<void>('POST', `/api/messages/${enc(id)}/reject`)
  const startNew = (m: Message) => deps.api<void>('POST', `/api/agents/${enc(m.workspace)}/${enc(m.to)}/start`, { new: true })
  const deliverNow = (m: Message) => deps.api<void>('POST', `/api/agents/${enc(m.workspace)}/${enc(m.to)}/deliver-now`)

  return { newestFirst, loading, error, counts, lit, start, stop, reload: () => (ws ? load(ws) : Promise.resolve()), approve, reject, startNew, deliverNow }
}

function upsert(list: Message[], m: Message): Message[] {
  const i = list.findIndex((x) => x.id === m.id)
  if (i < 0) return [...list, m]
  const next = list.slice()
  next[i] = m
  return next
}

const enc = encodeURIComponent

// clock formats an RFC 3339 time as local HH:MM:SS (or HH:MM).
export function clock(rfc3339: string, seconds = true): string {
  const d = new Date(rfc3339)
  if (Number.isNaN(d.getTime())) return '--:--'
  const p = (n: number) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  return seconds ? `${hm}:${p(d.getSeconds())}` : hm
}

export type Messages = ReturnType<typeof createMessages>

export const messages = createMessages()
