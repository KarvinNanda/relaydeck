<script setup lang="ts">
// The sidebar of mockup A: workspaces as a collapsible tree, one row per
// folder with its status, account, notes and message counts, then "Add
// workspace" and the status key. App.vue renders it inside <nav class="side">.
import { computed, nextTick, reactive, ref } from 'vue'
import { api, ApiError } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import Icon from './Icon.vue'
import AddFolderDialog, { nameError } from './AddFolderDialog.vue'
import { statusInfo, statusOrder } from './StatusBadge.vue'
import type { FolderState, WorkspaceState } from '../types'

const workspaces = computed(() => store.state.data?.workspaces ?? [])
// The server never sends an account on windows; hide it there in any case.
const windows = computed(() => store.state.data?.platform === 'windows')

const collapsed = reactive(new Set<string>())
const alert = ref('')
const dialogWs = ref<string | null>(null)

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

function toggle(ws: string): void {
  if (collapsed.has(ws)) collapsed.delete(ws)
  else collapsed.add(ws)
}

// Folders that need attention, shown on a collapsed workspace so the
// collapse never hides them.
function attentionCount(ws: WorkspaceState): number {
  return ws.folders.filter((f) => f.status === 'attention').length
}

function attentionText(n: number): string {
  return `${n} ${n === 1 ? 'needs' : 'need'} attention`
}

function isFocused(ws: string, f: FolderState): boolean {
  const p = panelStore.focused
  return p !== null && p.ws === ws && p.folder === f.name
}

function open(ws: string, f: FolderState): void {
  const r = panelStore.openPanel(ws, f.name)
  if (!r.ok) {
    alert.value = r.reason
    return
  }
  alert.value = ''
  store.ui.workspace = ws
}

// Remove a folder or a workspace (#116): one question at a time, right
// under what it removes. Only the config entry goes; the server refuses a
// running agent too, the check here only explains it before the click.
const rm = reactive({ ws: '', folder: null as string | null, open: false, sending: false, error: '' })
let rmCancel: HTMLButtonElement | null = null
let rmOpener: HTMLElement | null = null
function setRmCancel(el: unknown): void {
  rmCancel = el instanceof HTMLButtonElement ? el : null
}

function asking(ws: string, folder: string | null): boolean {
  return rm.open && rm.ws === ws && rm.folder === folder
}

async function askRemove(ws: string, folder: string | null, ev: Event): Promise<void> {
  rmOpener = ev.currentTarget instanceof HTMLElement ? ev.currentTarget : null
  Object.assign(rm, { ws, folder, open: true, error: '' })
  await nextTick()
  rmCancel?.focus() // the safe choice
}

function closeRemove(): void {
  rm.open = false
  rmOpener?.focus()
  rmOpener = null
}

// Folders of ws whose agent is not stopped, in list order.
function notStopped(ws: WorkspaceState): string[] {
  return ws.folders.filter((f) => f.status !== 'stopped').map((f) => f.name)
}

async function confirmRemove(): Promise<void> {
  if (!rm.open || rm.sending) return
  const ws = encodeURIComponent(rm.ws)
  const path = rm.folder === null ? `/api/workspaces/${ws}` : `/api/workspaces/${ws}/folders/${encodeURIComponent(rm.folder)}`
  rm.sending = true
  rm.error = ''
  try {
    await api('DELETE', path)
    rm.open = false
    rmOpener = null
    await store.refresh()
  } catch (e) {
    rm.error = e instanceof ApiError ? e.message : 'Something went wrong. Try again.'
  } finally {
    rm.sending = false
  }
}

// Add workspace: a small inline form.
const wsForm = reactive({ open: false, name: '', touched: false, submitted: false, sending: false, error: '' })
const wsInput = ref<HTMLInputElement | null>(null)
const wsNameError = computed(() => (wsForm.touched || wsForm.submitted ? nameError(wsForm.name) : ''))

async function openWsForm(): Promise<void> {
  Object.assign(wsForm, { open: true, name: '', touched: false, submitted: false, error: '' })
  await nextTick()
  wsInput.value?.focus()
}

async function addWorkspace(): Promise<void> {
  wsForm.submitted = true
  wsForm.error = ''
  if (nameError(wsForm.name)) {
    wsInput.value?.focus()
    return
  }
  wsForm.sending = true
  try {
    await api('POST', '/api/workspaces', { name: wsForm.name })
    await store.refresh()
    wsForm.open = false
  } catch (e) {
    wsForm.error = e instanceof ApiError ? e.message : 'Something went wrong. Try again.'
  } finally {
    wsForm.sending = false
  }
}
</script>

<template>
  <div class="sb">
    <h2 class="side-title">Workspaces</h2>

    <div v-if="alert" class="side-alert" role="alert">
      <Icon name="attn" />
      <span class="msg">{{ alert }}</span>
      <button class="ib" type="button" aria-label="Dismiss" title="Dismiss" @click="alert = ''">
        <Icon name="x" />
      </button>
    </div>

    <!-- Only this part scrolls; the footer stays at the bottom. -->
    <div class="sb-tree">
      <p v-if="workspaces.length === 0" class="empty">No workspaces yet. Add one below.</p>

      <div v-for="ws in workspaces" :key="ws.name" class="ws">
        <div class="ws-head">
          <button
            class="ws-row"
            type="button"
            :aria-expanded="!collapsed.has(ws.name)"
            :aria-controls="`ws-tree-${ws.name}`"
            @click="toggle(ws.name)"
          >
            <Icon :name="collapsed.has(ws.name) ? 'right' : 'down'" />
            <span class="name">{{ ws.name }}</span>
            <!-- Read as "4 folders · 1 needs attention". The attention part is
                 too wide for the name line, so it gets its own line under the
                 name and the "·" is only for screen readers. -->
            <span class="meta"><span class="cnt">{{ plural(ws.folders.length, 'folder', 'folders') }}</span><template v-if="collapsed.has(ws.name) && attentionCount(ws) > 0"><span class="sr-only"> · </span><span class="ws-attn c-attn"><Icon name="attn" />{{ attentionText(attentionCount(ws)) }}</span></template></span>
          </button>
          <button
            class="ib rm rm-ws"
            type="button"
            :aria-label="`Remove workspace ${ws.name}`"
            :title="`Remove workspace ${ws.name}`"
            :aria-expanded="asking(ws.name, null)"
            @click="askRemove(ws.name, null, $event)"
          >
            <Icon name="trash" />
          </button>
        </div>
        <div v-if="asking(ws.name, null)" class="rm-confirm rm-ws-confirm" role="group" :aria-label="`Remove workspace ${ws.name}`" @keydown.esc="closeRemove">
          <template v-if="notStopped(ws).length > 0">
            <p>Stop {{ notStopped(ws).join(', ') }} before you remove this workspace.</p>
            <div class="acts"><button :ref="setRmCancel" class="btn" type="button" @click="closeRemove">Close</button></div>
          </template>
          <template v-else>
            <p class="q">Remove workspace {{ ws.name }} and its {{ plural(ws.folders.length, 'folder', 'folders') }} from relaydeck?</p>
            <p class="hint">The folders and their Claude Code sessions stay on disk.</p>
            <p v-if="rm.error" class="srv-err" role="alert"><Icon name="attn" /><span>{{ rm.error }}</span></p>
            <div class="acts">
              <button :ref="setRmCancel" class="btn" type="button" @click="closeRemove">Cancel</button>
              <button class="btn btn-danger" type="button" :disabled="rm.sending" @click="confirmRemove"><Icon name="trash" />Remove</button>
            </div>
          </template>
        </div>

        <div v-if="!collapsed.has(ws.name)" :id="`ws-tree-${ws.name}`">
          <ul v-if="ws.folders.length" class="tree">
            <li v-for="f in ws.folders" :key="f.name" class="node">
              <button
                class="node-btn"
                :class="{ sel: isFocused(ws.name, f) }"
                type="button"
                :aria-current="isFocused(ws.name, f) ? 'true' : undefined"
                :title="f.path"
                @click="open(ws.name, f)"
              >
                <span class="node-name"><Icon :name="statusInfo(f.status).icon" :class="statusInfo(f.status).cls" />{{ f.name }}</span>
                <span class="node-meta"><span class="meta-in">
                  <span class="st" :class="statusInfo(f.status).cls">{{ statusInfo(f.status).word }}</span>
                  <span v-if="!windows" class="acct">{{ f.account || 'no account' }}</span>
                </span></span>
                <span v-if="f.pending > 0 || f.awaiting_approval > 0" class="chips">
                  <span v-if="f.pending > 0" class="chip"><Icon name="msg" />{{ plural(f.pending, 'message', 'messages') }} waiting</span>
                  <span v-if="f.awaiting_approval > 0" class="chip amber"><Icon name="shield" />{{ f.awaiting_approval }} awaiting approval</span>
                </span>
                <span v-if="f.on_default_login && !windows" class="note"><Icon name="attn" /><span>On default login: a cswap switch will move it</span></span>
              </button>
              <button
                class="ib rm"
                type="button"
                :aria-label="`Remove folder ${f.name}`"
                :title="`Remove folder ${f.name}`"
                :aria-expanded="asking(ws.name, f.name)"
                @click="askRemove(ws.name, f.name, $event)"
              >
                <Icon name="trash" />
              </button>
              <div v-if="asking(ws.name, f.name)" class="rm-confirm" role="group" :aria-label="`Remove folder ${f.name}`" @keydown.esc="closeRemove">
                <template v-if="f.status !== 'stopped'">
                  <p>Stop {{ f.name }} before you remove it.</p>
                  <div class="acts"><button :ref="setRmCancel" class="btn" type="button" @click="closeRemove">Close</button></div>
                </template>
                <template v-else>
                  <p class="q">Remove {{ f.name }} from relaydeck?</p>
                  <p class="hint">The folder and its Claude Code sessions stay on disk.</p>
                  <p v-if="rm.error" class="srv-err" role="alert"><Icon name="attn" /><span>{{ rm.error }}</span></p>
                  <div class="acts">
                    <button :ref="setRmCancel" class="btn" type="button" @click="closeRemove">Cancel</button>
                    <button class="btn btn-danger" type="button" :disabled="rm.sending" @click="confirmRemove"><Icon name="trash" />Remove</button>
                  </div>
                </template>
              </div>
            </li>
          </ul>
          <div class="add-row">
            <button class="btn add-folder" type="button" @click="dialogWs = ws.name">
              <Icon name="fplus" />Add folder
            </button>
          </div>
        </div>
      </div>

      <!-- Last in the scroll box: it sits right under the list, and sticks to
           the bottom edge once the list is longer than the sidebar (#96). -->
      <div class="ws-add">
        <form v-if="wsForm.open" class="ws-form" novalidate @submit.prevent="addWorkspace">
          <label for="new-ws">Workspace name</label>
          <input
            id="new-ws"
            ref="wsInput"
            v-model.trim="wsForm.name"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="e.g. game-lounge"
            :aria-invalid="wsNameError ? 'true' : undefined"
            :aria-describedby="wsNameError ? 'new-ws-err' : undefined"
            @blur="wsForm.touched = true"
            @keydown.esc="wsForm.open = false"
          >
          <p v-if="wsNameError" id="new-ws-err" class="err">{{ wsNameError }}</p>
          <p v-if="wsForm.error" class="srv-err" role="alert"><Icon name="attn" /><span>{{ wsForm.error }}</span></p>
          <div class="acts">
            <button class="btn" type="button" @click="wsForm.open = false">Cancel</button>
            <button class="btn btn-primary" type="submit" :disabled="wsForm.sending"><Icon name="plus" />Add</button>
          </div>
        </form>
        <button v-else class="btn add-ws" type="button" @click="openWsForm"><Icon name="plus" />Add workspace</button>
      </div>
    </div>

    <div class="side-foot">
      <div class="legend" role="group" aria-label="Status key">
        <span v-for="s in statusOrder" :key="s" :class="statusInfo(s).cls"><Icon :name="statusInfo(s).icon" />{{ statusInfo(s).word }}</span>
      </div>
    </div>

    <AddFolderDialog v-if="dialogWs" :ws="dialogWs" @close="dialogWs = null" />
  </div>
</template>

<style scoped>
.sb { display: flex; flex-direction: column; flex: 1 1 auto; min-width: 0; min-height: 0; }
/* the tree scrolls inside the sidebar; the footer below it stays put */
.sb-tree {
  flex: 1 1 auto; min-height: 0; overflow-y: auto;
  scrollbar-width: thin; scrollbar-color: var(--border-strong) transparent;
  /* Shade at the top or bottom edge only while there is more to scroll to:
     the "local" covers move with the content and hide the "scroll" shades
     once the end is reached. */
  background:
    linear-gradient(var(--surface) 30%, transparent) center top / 100% 16px local no-repeat,
    linear-gradient(transparent, var(--surface) 70%) center bottom / 100% 16px local no-repeat,
    linear-gradient(var(--border-strong), transparent) center top / 100% 8px scroll no-repeat,
    linear-gradient(transparent, var(--border-strong)) center bottom / 100% 8px scroll no-repeat;
}
/* focus rings drawn inside, so the scroll box does not cut them off */
.sb-tree :focus-visible { outline-offset: -2px; }
.side-title { padding: 0 var(--s-5) var(--s-3); color: var(--text-muted); font-weight: 400; }

.side-alert {
  display: flex; align-items: flex-start; gap: var(--s-3);
  margin: 0 var(--s-5) var(--s-4); padding: var(--s-2) var(--s-2) var(--s-2) var(--s-4);
  border: 1px solid var(--amber-line); background: var(--amber-tint); color: var(--amber); border-radius: var(--r-sm);
}
.side-alert > .i { margin-top: 8px; }
.side-alert .msg { flex: 1; padding: var(--s-3) 0; }
.side-alert .ib { color: var(--amber); height: 28px; min-width: 28px; }

/* workspace row */
.ws-row {
  width: 100%; display: flex; flex-wrap: wrap; align-items: center; gap: 0 var(--s-3);
  min-height: var(--row-h); padding: var(--s-1) var(--s-5); border: 0; background: none; cursor: pointer; text-align: left;
}
.ws-row:hover { background: var(--muted); }
.ws-row .name { font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-row .meta { display: contents; }
.ws-row .cnt { margin-left: auto; color: var(--text-muted); white-space: nowrap; }
/* own line, lined up with the workspace name (icon 14px + gap 6px) */
.ws-row .ws-attn { flex-basis: 100%; display: flex; align-items: center; gap: var(--s-2); padding-left: 20px; }

/* folder tree */
.tree { list-style: none; margin: 0 0 var(--s-2); padding: 0 var(--s-3) 0 22px; position: relative; }
.tree::before { content: ""; position: absolute; left: 16px; top: 0; bottom: 6px; border-left: 1px solid var(--border); }
.node { margin-bottom: var(--s-1); }
.node-btn {
  width: 100%; display: flex; flex-direction: column; align-items: stretch; text-align: left;
  padding: var(--s-3) var(--s-4); border-radius: var(--r); border: 1px solid transparent; background: none; cursor: pointer;
}
.node-btn:hover { background: var(--muted); }
.node-btn.sel { background: var(--muted); border-color: var(--border-strong); }
.node-name { display: flex; align-items: center; gap: var(--s-3); font-weight: 600; min-width: 0; overflow-wrap: anywhere; }
/* "status · account". Each part has a 3ch left margin that holds the "·";
   .meta-in is pulled 3ch left and .node-meta clips it, so the part that
   starts a line has its margin and dot cut off. A wrapped account then sits
   on its own line without a dot, as personal-1 does in mockup A. */
.node-meta { display: block; margin-left: 20px; overflow: hidden; color: var(--text-muted); }
.meta-in { display: flex; flex-wrap: wrap; margin-left: -3ch; }
.meta-in > span { margin-left: 3ch; min-width: 0; overflow-wrap: anywhere; }
.meta-in .st { font-weight: 500; }
.acct::before { content: "·"; display: inline-block; width: 3ch; margin-left: -3ch; text-align: center; }
.chips { display: flex; flex-wrap: wrap; gap: var(--s-2); margin: var(--s-2) 0 0 20px; }
.chip.amber { border-color: var(--amber-line); background: var(--amber-tint); color: var(--amber); }
.note { display: flex; gap: var(--s-3); align-items: flex-start; margin: var(--s-3) 0 0 20px; }
.note .i { margin-top: 2px; }

.add-row { padding: var(--s-2) var(--s-3) var(--s-4) 22px; }

/* remove (#116): always visible but quiet, red only on hover or focus, so
   it never depends on hover (touch). It sits over the top right corner of
   its row, so only the name line gives up room for it, and its right edge
   lines up with the sidebar content edge (10px from the border). */
.node { position: relative; }
.node-name { padding-right: 20px; }
.rm { height: 24px; min-width: 24px; padding: 0; color: var(--text-muted); }
.node > .rm { position: absolute; top: var(--s-2); right: var(--s-2); }
.ws-head { position: relative; }
.ws-head .ws-row { padding-right: calc(var(--s-5) + 24px + var(--s-3)); }
.ws-head .rm { position: absolute; top: var(--s-2); right: var(--s-5); }
.rm:hover:not(:disabled), .rm:focus-visible, .rm[aria-expanded="true"] { color: var(--red); background: var(--red-tint); border-color: var(--red-line); }
.rm-confirm {
  display: grid; gap: var(--s-2); margin: var(--s-1) var(--s-2) var(--s-3) 0; padding: var(--s-3) var(--s-4);
  border: 1px solid var(--border-strong); border-radius: var(--r-sm); background: var(--bg);
}
.rm-ws-confirm { margin: var(--s-1) var(--s-5) var(--s-4) var(--s-5); }
.rm-confirm p { margin: 0; overflow-wrap: anywhere; }
.rm-confirm .q { font-weight: 600; }
.rm-confirm .hint { color: var(--text-muted); }
.rm-confirm .acts { display: flex; justify-content: flex-end; gap: var(--s-3); flex-wrap: wrap; }
.add-folder { background: transparent; border-style: dashed; color: var(--text); padding: 0 var(--s-4); }

.ws-add {
  position: sticky; bottom: 0; z-index: 1;
  padding: var(--s-4) var(--s-5); background: var(--surface);
}
/* While the list scrolls under it, the rows fade out above the button, so it
   still shows that there is more below (the tree's own bottom shade is
   covered). Over the empty gap of a short list it is invisible. */
.ws-add::before {
  content: ""; position: absolute; left: 0; right: 0; bottom: 100%; height: 16px;
  background: linear-gradient(transparent, var(--surface)); pointer-events: none;
}
.add-ws { justify-content: center; width: 100%; }
/* footer: the status key */
.side-foot {
  flex: none; padding: var(--s-4) var(--s-5) 0; border-top: 1px solid var(--border);
}
.legend { display: flex; flex-wrap: wrap; gap: var(--s-1) var(--s-5); font-size: 12px; }
@media (max-width: 767px) { .legend { font-size: 14px; gap: var(--s-2) var(--s-6); } }
.legend span { display: inline-flex; align-items: center; gap: 5px; }

.ws-form { display: grid; gap: var(--s-2); }
.ws-form label { font-weight: 600; }
.ws-form p { margin: 0; }
.ws-form input {
  height: var(--control-h); width: 100%; min-width: 0; padding: 0 var(--s-4);
  background: var(--bg); border: 1px solid var(--border); border-radius: var(--r);
}
.ws-form input::placeholder { color: var(--text-muted); }
.ws-form input[aria-invalid="true"] { border-color: var(--red); }
.err { color: var(--red); }
.srv-err {
  display: flex; gap: var(--s-3); align-items: flex-start;
  padding: var(--s-3) var(--s-4); border-radius: var(--r-sm);
  border: 1px solid var(--red-line); background: var(--red-tint); color: var(--red); overflow-wrap: anywhere;
}
.srv-err .i { margin-top: 2px; }
.ws-form .acts { display: flex; justify-content: flex-end; gap: var(--s-4); margin-top: var(--s-2); }
.btn:disabled { opacity: .6; cursor: default; }

@media (max-width: 767px) {
  /* the stacked sidebar has a 6px side padding; the footer rule runs the
     full width like the top bar rule, and the text stays where it was */
  .side-foot { margin-inline: calc(-1 * var(--s-3)); padding-inline: calc(var(--s-5) + var(--s-3)); }
  /* the stacked sidebar does not scroll on its own and fades into the page
     color, so the button needs no sticky frame there */
  .ws-add { position: static; background: none; }
  .ws-add::before { display: none; }
  .ws-row { min-height: var(--touch-h); }
  .node-btn { min-height: var(--touch-h); }
  .rm { height: var(--touch-h); min-width: var(--touch-h); }
  .node > .rm { top: 0; }
  .node-name { padding-right: 38px; }
  .ws-head .ws-row { padding-right: calc(var(--s-5) + var(--touch-h) + var(--s-3)); }
  .ws-head .rm { top: 0; }
  .rm-confirm .acts .btn { flex: 1; justify-content: center; min-height: var(--touch-h); }
  .side-alert .ib { height: var(--touch-h); min-width: var(--touch-h); }
  .side-alert > .i { margin-top: 15px; }
  .side-alert .msg { padding: 12px 0; }
  .ws-form input { height: var(--touch-h); font-size: 16px; }
  .ws-form .acts .btn { flex: 1; justify-content: center; }
}
</style>
