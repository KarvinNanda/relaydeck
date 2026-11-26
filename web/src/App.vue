<script setup lang="ts">
// The app frame from mockup A: top bar, sidebar, terminal panels and the
// Messages drawer. Each region renders one component that Tasks 16, 17 and
// 18 own, so this file does not change when they fill them in.
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { store } from './stores/state'
import { panelStore } from './stores/panels'
import { prefs, setSideWidth, SIDE_DEFAULT, SIDE_MAX, SIDE_MIN } from './stores/prefs'
import Icon from './components/Icon.vue'
import NoToken from './components/NoToken.vue'
import Sidebar from './components/Sidebar.vue'
import PanelGrid from './components/PanelGrid.vue'
import NotifyView from './components/NotifyView.vue'
import FontSize from './components/FontSize.vue'

const host = window.location.host

const workspace = computed(() => store.activeWorkspace())

// Messages held at the hop limit in this workspace: they wait for the user.
const awaiting = computed(() => (workspace.value?.folders ?? []).reduce((n, f) => n + f.awaiting_approval, 0))

// The word carries the meaning; color and dot shape only repeat it.
const connClass = computed(() => {
  switch (store.state.connection) {
    case 'connected':
      return 'c-busy'
    case 'connecting':
      return 'c-start'
    default:
      return 'c-stop'
  }
})

// A click is the user's choice and is kept per browser. An automatic open
// (below) is not, so the drawer still starts closed on the next load.
function toggleMessages(): void {
  store.ui.messagesOpen = !store.ui.messagesOpen
  prefs.messagesOpen = store.ui.messagesOpen
}

// A message held at the hop limit needs the user, so the drawer opens by
// itself when the active workspace gets one. Other messages wait in it.
// Only a rise inside the same workspace counts: switching to a workspace that
// already has approvals waiting is not news (#79).
watch(
  () => [workspace.value?.name, awaiting.value] as const,
  ([name, n], [oldName, old]) => {
    if (name === oldName && n > old) store.ui.messagesOpen = true
  },
)

// A folder or workspace removed (in this tab or another) closes its panels.
// Before the first state arrives nothing is known, so nothing closes.
watch(
  () => store.state.data,
  (data) => {
    if (!data) return
    panelStore.prune((ws, folder) => data.workspaces.some((w) => w.name === ws && w.folders.some((f) => f.name === folder)))
  },
  { immediate: true },
)

// ---- sidebar width ----
// The user sets the width with the separator (drag, arrow keys, Home/End,
// double click resets) or hides the sidebar. Both are kept per browser.
const gridCols = computed(() => `${prefs.sideHidden ? 0 : prefs.sideWidth}px minmax(0, 1fr)`)
const KEY_STEP = 16

function toggleSide(): void {
  prefs.sideHidden = !prefs.sideHidden
}

let drag: { id: number; x: number; w: number } | null = null
function onSepDown(ev: PointerEvent): void {
  if (ev.button !== 0) return
  ev.preventDefault() // no text selection while dragging
  drag = { id: ev.pointerId, x: ev.clientX, w: prefs.sideWidth }
  ;(ev.currentTarget as HTMLElement).setPointerCapture?.(ev.pointerId)
}
// A move counts only while the primary button is down: if the pointer was
// lost (Cmd-Tab, a system dialog) no pointerup arrives, and a mouse keeps
// pointerId 1, so plain hover moves would otherwise drag the width (#78).
function onSepMove(ev: PointerEvent): void {
  if (drag && ev.pointerId === drag.id && ev.buttons & 1) setSideWidth(drag.w + ev.clientX - drag.x)
}
function onSepLost(): void {
  drag = null
}
function onSepUp(ev: PointerEvent): void {
  if (!drag || ev.pointerId !== drag.id) return
  drag = null
  const el = ev.currentTarget as HTMLElement
  if (el.hasPointerCapture?.(ev.pointerId)) el.releasePointerCapture(ev.pointerId)
}
function onSepKey(ev: KeyboardEvent): void {
  const next: Record<string, number> = {
    ArrowLeft: prefs.sideWidth - KEY_STEP,
    ArrowRight: prefs.sideWidth + KEY_STEP,
    Home: SIDE_MIN,
    End: SIDE_MAX,
  }
  if (!(ev.key in next)) return
  ev.preventDefault()
  setSideWidth(next[ev.key])
}

// A file dropped where no panel takes it would make the browser open the file
// in this tab and leave relaydeck. Panels handle their own drops first.
function blockFileDrop(ev: DragEvent): void {
  if (ev.dataTransfer?.types?.includes('Files')) ev.preventDefault()
}
onMounted(() => {
  window.addEventListener('dragover', blockFileDrop)
  window.addEventListener('drop', blockFileDrop)
})
onBeforeUnmount(() => {
  window.removeEventListener('dragover', blockFileDrop)
  window.removeEventListener('drop', blockFileDrop)
})
</script>

<template>
  <NoToken v-if="store.state.unauthorized" />
  <div v-else class="app" :class="{ 'side-hidden': prefs.sideHidden }" :style="{ gridTemplateColumns: gridCols }">
    <header class="top">
      <button
        class="ib side-toggle"
        type="button"
        :aria-expanded="prefs.sideHidden ? 'false' : 'true'"
        aria-controls="sidebar"
        :aria-label="prefs.sideHidden ? 'Show sidebar' : 'Hide sidebar'"
        :title="prefs.sideHidden ? 'Show sidebar' : 'Hide sidebar'"
        @click="toggleSide"
      >
        <Icon name="sidebar" />
      </button>
      <div class="brand"><Icon name="term" />relaydeck</div>
      <div class="crumb">
        <Icon name="layers" />workspace
        <b v-if="workspace">{{ workspace.name }}</b>
        <span v-else>none yet</span>
      </div>
      <div class="spacer"></div>
      <FontSize />
      <!-- Task 17 renders the layout switcher here with <Teleport defer to="#layout-slot">. -->
      <div id="layout-slot" class="layout-slot"></div>
      <button
        class="btn msg-toggle"
        type="button"
        :aria-pressed="store.ui.messagesOpen"
        aria-controls="messages"
        @click="toggleMessages"
      >
        <Icon name="msg" />Messages
        <span v-if="awaiting > 0" class="count" aria-hidden="true">{{ awaiting }}</span>
        <span v-if="awaiting > 0" class="sr-only">, {{ awaiting }} awaiting approval</span>
      </button>
      <div class="conn" :class="connClass" role="status" :title="`relaydeck server on ${host}`">
        <span class="dot" :class="store.state.connection" aria-hidden="true"></span>
        <b>{{ store.state.connection }}</b>
        <span class="host">{{ host }}</span>
      </div>
    </header>

    <nav id="sidebar" class="side" aria-label="Workspaces">
      <Sidebar />
    </nav>
    <div
      v-if="!prefs.sideHidden"
      class="side-sep"
      role="separator"
      aria-orientation="vertical"
      aria-label="Sidebar width"
      :aria-valuemin="SIDE_MIN"
      :aria-valuemax="SIDE_MAX"
      :aria-valuenow="prefs.sideWidth"
      tabindex="0"
      title="Drag to resize the sidebar. Double click resets it."
      @pointerdown="onSepDown"
      @pointermove="onSepMove"
      @pointerup="onSepUp"
      @pointercancel="onSepUp"
      @lostpointercapture="onSepLost"
      @keydown="onSepKey"
      @dblclick="setSideWidth(SIDE_DEFAULT)"
    ></div>

    <main class="main">
      <section class="panels" aria-label="Terminals">
        <PanelGrid />
      </section>
      <section v-show="store.ui.messagesOpen" id="messages" class="drawer" aria-label="Messages">
        <NotifyView />
      </section>
    </main>
  </div>
</template>

<style scoped>
.app {
  height: 100vh;
  min-height: 520px;
  display: grid;
  grid-template-columns: var(--side-w) minmax(0, 1fr);
  grid-template-rows: var(--top-h) minmax(0, 1fr);
}

/* top bar */
.top {
  grid-column: 1 / -1;
  display: flex; align-items: center; gap: var(--s-6);
  padding: 0 var(--s-5) 0 var(--s-3);
  border-bottom: 1px solid var(--border);
  background: var(--surface);
  min-width: 0;
}
.brand { display: flex; align-items: center; gap: var(--s-4); font-weight: 700; }
.brand .i { width: 16px; height: 16px; color: var(--green); }
.crumb { display: flex; align-items: center; gap: var(--s-3); color: var(--text-muted); min-width: 0; }
.crumb b { color: var(--text); font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.spacer { flex: 1; }
.layout-slot { display: flex; align-items: center; gap: var(--s-2); }
.layout-slot:empty { display: none; }
.conn { display: flex; align-items: center; gap: var(--s-3); white-space: nowrap; }
.conn b { font-weight: 500; }
.conn .host { color: var(--text-muted); }
/* filled dot when connected or disconnected, a ring while connecting */
.dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; flex: none; }
.dot.connecting { background: transparent; border: 2px solid currentColor; }

/* sidebar: its width is the first grid column (inline style from prefs) */
.side-toggle { flex: none; }
/* the label says what a click does; no pressed or expanded fill (#76) */
.side-toggle[aria-expanded] { background: none; border-color: transparent; color: var(--text-muted); }
.side-toggle:hover { background: var(--muted); color: var(--text); }
.app.side-hidden .side { display: none; }
/* the resize handle sits on the sidebar's right border, over both columns */
.side-sep {
  grid-column: 1; grid-row: 2; justify-self: end; position: relative; z-index: 3;
  width: 7px; margin-right: -4px; cursor: col-resize; touch-action: none;
}
.side-sep::after {
  content: ""; position: absolute; top: 0; bottom: 0; left: 3px; width: 1px; background: transparent;
  transition: background .15s;
}
.side-sep:hover::after, .side-sep:focus-visible::after { background: var(--blue); }
.side-sep:focus-visible { outline: none; }

/* sidebar */
.side {
  /* placed explicitly: the separator also sits in column 1, row 2 */
  grid-column: 1; grid-row: 2;
  border-right: 1px solid var(--border);
  background: var(--surface);
  overflow-y: auto;
  display: flex; flex-direction: column;
  padding: var(--s-4) 0;
  min-height: 0;
}

/* main: panels above, Messages drawer below */
.main { grid-column: 2; grid-row: 2; display: grid; grid-template-rows: minmax(0, 1fr) auto; min-width: 0; min-height: 0; }
.panels { min-height: 0; min-width: 0; }
.drawer {
  border-top: 1px solid var(--border-strong);
  background: var(--surface);
  height: var(--drawer-h);
  display: flex; flex-direction: column;
  min-height: 0;
}

/* phone: one column, top bar wraps, regions stack */
@media (max-width: 767px) {
  .app { height: auto; min-height: 100vh; display: flex; flex-direction: column; }
  .top { flex-wrap: wrap; padding: var(--s-4) var(--gutter-mobile); gap: var(--s-4) var(--s-6); }
  .top .spacer { display: none; }
  /* panels stack on a phone: no width to set, and the sidebar always shows */
  .side-toggle, .side-sep { display: none; }
  .app.side-hidden .side { display: flex; }
  .conn { order: 1; margin-left: auto; }
  .msg-toggle { order: 2; }
  .crumb { order: 3; }
  .layout-slot { order: 4; width: 100%; }
  /* K-blend: the stacked sidebar fades into the page color over its last
     part, eased, instead of a hard edge above the panels (#77). */
  .side {
    border-right: 0; border-bottom: 0; padding: var(--s-6) var(--s-3) var(--s-8);
    background: linear-gradient(
      to bottom,
      var(--surface) 0%, var(--surface) 70%,
      color-mix(in srgb, var(--surface) 82%, var(--bg)) 78%,
      color-mix(in srgb, var(--surface) 55%, var(--bg)) 86%,
      color-mix(in srgb, var(--surface) 25%, var(--bg)) 94%,
      var(--bg) 100%
    );
  }
  /* the text size control sits at the right of the first top bar row */
  .fs { margin-left: auto; }
  .main { display: flex; flex-direction: column; }
  .drawer { height: auto; border-bottom: 1px solid var(--border); }
}
</style>
