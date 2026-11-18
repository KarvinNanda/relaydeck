<script setup lang="ts">
// Stub. Task 18 replaces this file in full (timeline, graph, approve and
// reject). App.vue renders it inside the Messages drawer and hides the drawer
// when store.ui.messagesOpen is false.
import { store } from '../stores/state'
import Icon from './Icon.vue'
</script>

<template>
  <div class="dh">
    <button class="ib" type="button" aria-label="Collapse Messages" title="Collapse Messages" @click="store.ui.messagesOpen = false">
      <Icon name="down" />
    </button>
    <h2>Messages</h2>
  </div>
  <p class="empty">No messages yet.</p>
</template>

<style scoped>
.dh {
  display: flex; align-items: center; gap: var(--s-5); min-height: 34px;
  padding: 0 var(--s-5) 0 var(--s-2); border-bottom: 1px solid var(--border);
}
.dh h2 { font-weight: 700; }
@media (max-width: 767px) {
  .dh { padding: var(--s-4) var(--gutter-mobile) var(--s-4) var(--s-4); }
  .empty { padding: var(--s-4) var(--gutter-mobile); }
}
</style>
