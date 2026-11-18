<script lang="ts">
// relTime says how long ago an RFC 3339 time was, in the words of mockup A.
export function relTime(iso: string, now: Date = new Date()): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ''
  const min = Math.floor((now.getTime() - t) / 60000)
  if (min < 1) return 'just now'
  if (min < 60) return `${min} min ago`
  const h = Math.floor(min / 60)
  if (h < 24) return `${h} h ago`
  if (h < 48) return 'yesterday'
  const d = Math.floor(h / 24)
  if (d < 7) return `${d} days ago`
  return new Date(t).toISOString().slice(0, 10)
}
</script>

<script setup lang="ts">
// The session picker of one folder: its Claude Code sessions, newest first,
// the current one marked, and "New session". A self-contained popover box:
// the parent (the panel header, Task 17) decides where it sits.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import { store } from '../stores/state'
import { MAX_PANELS, panelStore, tooManyPanels } from '../stores/panels'
import Icon from './Icon.vue'
import type { Session } from '../types'

const props = defineProps<{ ws: string; folder: string }>()
const emit = defineEmits<{ close: [] }>()

const sessions = ref<Session[] | null>(null)
const error = ref('')
const starting = ref(false)
const newBtn = ref<HTMLButtonElement | null>(null)
let returnFocus: HTMLElement | null = null

const current = computed(
  () =>
    store.state.data?.workspaces.find((w) => w.name === props.ws)?.folders.find((f) => f.name === props.folder)
      ?.session_id ?? '',
)

const sorted = computed(() =>
  [...(sessions.value ?? [])].sort((a, b) => (Date.parse(b.modified) || 0) - (Date.parse(a.modified) || 0)),
)

function message(e: unknown): string {
  return e instanceof ApiError ? e.message : 'Something went wrong. Try again.'
}

const base = computed(() => `${encodeURIComponent(props.ws)}/${encodeURIComponent(props.folder)}`)

async function load(): Promise<void> {
  try {
    const r = await api<{ sessions: Session[] }>(
      'GET',
      `/api/workspaces/${encodeURIComponent(props.ws)}/folders/${encodeURIComponent(props.folder)}/sessions`,
    )
    sessions.value = r?.sessions ?? []
  } catch (e) {
    error.value = message(e)
  }
}

// The panel can open when it is open already or the grid has room.
function panelFits(): boolean {
  const open = panelStore.panels.some((p) => p.ws === props.ws && p.folder === props.folder)
  return open || panelStore.panels.length < MAX_PANELS
}

async function start(body: { session_id: string } | { new: true }): Promise<void> {
  // Do not start an agent whose panel cannot be shown.
  if (!panelFits()) {
    error.value = tooManyPanels
    return
  }
  error.value = ''
  starting.value = true
  try {
    await api('POST', `/api/agents/${base.value}/start`, body)
  } catch (e) {
    error.value = message(e)
    return
  } finally {
    starting.value = false
  }
  // Checked again: another panel may have opened while start ran.
  const r = panelStore.openPanel(props.ws, props.folder)
  if (!r.ok) {
    error.value = r.reason
    return
  }
  emit('close')
}

function onKey(ev: KeyboardEvent): void {
  if (ev.key === 'Escape') emit('close')
}

onMounted(() => {
  returnFocus = document.activeElement as HTMLElement | null
  document.addEventListener('keydown', onKey)
  newBtn.value?.focus()
  void load()
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  returnFocus?.focus?.()
})
</script>

<template>
  <div class="pop" role="dialog" aria-label="Sessions">
    <div class="pop-h">
      <span class="pop-t"><b>Sessions</b> <span class="c-muted">{{ folder }}</span></span>
      <button ref="newBtn" class="btn btn-primary new" type="button" :disabled="starting" @click="start({ new: true })">
        <Icon name="plus" />New session
      </button>
      <button class="ib" type="button" aria-label="Close sessions" title="Close" @click="emit('close')">
        <Icon name="x" />
      </button>
    </div>

    <p v-if="error" class="pop-err" role="alert"><Icon name="attn" /><span>{{ error }}</span></p>

    <p v-if="sessions === null && !error" class="empty">Loading sessions…</p>
    <p v-else-if="sessions !== null && sorted.length === 0" class="empty">No sessions yet. Start a new one.</p>
    <ul v-else-if="sorted.length" class="list">
      <li v-for="s in sorted" :key="s.id" class="sess" :class="{ cur: s.id === current }">
        <button type="button" :disabled="starting" @click="start({ session_id: s.id })">
          <span class="t" :class="{ 'c-muted': !s.title }">{{ s.title || '(no title)' }}</span>
          <span v-if="s.id === current" class="tag">current</span>
          <span class="m">{{ relTime(s.modified) }} · {{ s.id.slice(0, 8) }}</span>
        </button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.pop {
  width: 320px; max-width: 100%;
  background: var(--muted); border: 1px solid var(--border-strong); border-radius: var(--r);
}
.pop-h {
  display: flex; align-items: center; gap: var(--s-3);
  padding: var(--s-2) var(--s-2) var(--s-2) var(--s-5); border-bottom: 1px solid var(--border-strong);
}
.pop-t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pop-t b { font-weight: 600; }
.btn:disabled { opacity: .6; cursor: default; }
.list { list-style: none; margin: 0; padding: var(--s-2); display: grid; gap: var(--s-1); max-height: 320px; overflow-y: auto; }
.sess button {
  width: 100%; display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 0 var(--s-4); align-items: center;
  padding: var(--s-1) var(--s-4); line-height: 16px; text-align: left;
  border-radius: var(--r-sm); border: 1px solid transparent; background: none; cursor: pointer;
}
.sess button:hover:not(:disabled) { background: var(--muted-hover); }
.sess button:disabled { cursor: default; }
.sess.cur button { border-color: var(--green-line); background: var(--green-tint); }
.t { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.m { grid-column: 1 / -1; color: var(--text-muted); }
.tag { padding: 0 var(--s-3); border-radius: var(--r-sm); background: var(--green); color: var(--on-accent); font-weight: 600; }
.pop-err {
  display: flex; gap: var(--s-3); align-items: flex-start; margin: var(--s-2) var(--s-2) 0;
  padding: var(--s-3) var(--s-4); border-radius: var(--r-sm);
  border: 1px solid var(--red-line); background: var(--red-tint); color: var(--red); overflow-wrap: anywhere;
}
.pop-err .i { margin-top: 2px; }
.empty { margin: 0; }

@media (max-width: 767px) {
  .pop { width: 100%; }
  /* the folder name keeps the first row; New session gets a full row */
  .pop-h { flex-wrap: wrap; padding: var(--s-2) var(--s-3) var(--s-4) var(--s-5); }
  .pop-h .new { order: 2; flex: 1 0 100%; justify-content: center; }
  .sess button { min-height: var(--touch-h); }
}
</style>
