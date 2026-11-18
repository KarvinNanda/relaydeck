<script setup lang="ts">
// The Messages drawer from mockup A: a header with the counts that need the
// user, the timeline (about two thirds) and the graph (about one third).
// App.vue renders it inside section#messages and hides the drawer when
// store.ui.messagesOpen is false.
import { computed, onMounted, onUnmounted } from 'vue'
import { messages } from '../stores/messages'
import { store } from '../stores/state'
import Icon from './Icon.vue'
import NotifyGraph from './NotifyGraph.vue'
import Timeline from './Timeline.vue'

const workspace = computed(() => store.activeWorkspace())
const counts = messages.counts

onMounted(() => messages.start())
onUnmounted(() => messages.stop())
</script>

<template>
  <div class="dh">
    <button
      class="btn-sq"
      type="button"
      aria-label="Collapse Messages"
      title="Collapse Messages"
      aria-controls="messages"
      :aria-expanded="store.ui.messagesOpen"
      @click="store.ui.messagesOpen = false"
    >
      <Icon name="down" />
    </button>
    <h2>Messages</h2>
    <div class="sum">
      <span v-if="counts.awaiting > 0" class="c-attn"><Icon name="shield" />{{ counts.awaiting }} awaiting approval</span>
      <span v-if="counts.held > 0" class="c-attn"><Icon name="pause" />{{ counts.held }} held</span>
      <span v-if="counts.waiting > 0" class="c-idle"><Icon name="clock" />{{ counts.waiting }} waiting</span>
    </div>
  </div>
  <div class="db">
    <Timeline :messages="messages" />
    <NotifyGraph v-if="workspace" :messages="messages" :folders="workspace.folders" :workspace="workspace.name" />
  </div>
</template>

<style scoped>
/* The chevron starts at the same inset as the timeline text (--s-6). */
.dh {
  display: flex; flex-wrap: wrap; align-items: center; gap: var(--s-2) var(--s-5); min-height: 34px;
  padding: var(--s-1) var(--s-5) var(--s-1) var(--s-6); border-bottom: 1px solid var(--border);
}
.dh h2 { font-weight: 700; }
.sum { display: flex; flex-wrap: wrap; gap: var(--s-2) var(--s-6); }
.sum:empty { display: none; }
.sum > span { display: inline-flex; align-items: center; gap: 5px; white-space: nowrap; }

.db {
  flex: 1; min-height: 0;
  display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); grid-template-rows: minmax(0, 1fr);
}
.db > .tl { border-right: 1px solid var(--border); }

@media (max-width: 767px) {
  .dh { padding: var(--s-4) var(--gutter-mobile); }
  /* the three counts wrap onto a second line when they do not fit */
  .sum { gap: var(--s-2) var(--s-4); }
  .sum > span { gap: var(--s-2); }
  .db { display: block; }
  .db > .tl { border-right: 0; border-bottom: 1px solid var(--border); }
}
</style>
