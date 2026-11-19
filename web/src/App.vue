<script setup lang="ts">
// The app frame from mockup A: top bar, sidebar, terminal panels and the
// Messages drawer. Each region renders one component that Tasks 16, 17 and
// 18 own, so this file does not change when they fill them in.
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { store } from './stores/state'
import Icon from './components/Icon.vue'
import NoToken from './components/NoToken.vue'
import Sidebar from './components/Sidebar.vue'
import PanelGrid from './components/PanelGrid.vue'
import NotifyView from './components/NotifyView.vue'

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

function toggleMessages(): void {
  store.ui.messagesOpen = !store.ui.messagesOpen
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
  <div v-else class="app">
    <header class="top">
      <div class="brand"><Icon name="term" />relaydeck</div>
      <div class="crumb">
        <Icon name="layers" />workspace
        <b v-if="workspace">{{ workspace.name }}</b>
        <span v-else>none yet</span>
      </div>
      <div class="spacer"></div>
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

    <nav class="side" aria-label="Workspaces">
      <Sidebar />
    </nav>

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
  padding: 0 var(--s-5) 0 var(--s-6);
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

/* sidebar */
.side {
  border-right: 1px solid var(--border);
  background: var(--surface);
  overflow-y: auto;
  display: flex; flex-direction: column;
  padding: var(--s-4) 0;
  min-height: 0;
}

/* main: panels above, Messages drawer below */
.main { display: grid; grid-template-rows: minmax(0, 1fr) auto; min-width: 0; min-height: 0; }
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
  .conn { order: 1; margin-left: auto; }
  .msg-toggle { order: 2; }
  .crumb { order: 3; }
  .layout-slot { order: 4; width: 100%; }
  .side { border-right: 0; border-bottom: 1px solid var(--border); padding: var(--s-6) var(--s-3); }
  .main { display: flex; flex-direction: column; }
  .drawer { height: auto; border-bottom: 1px solid var(--border); }
}
</style>
