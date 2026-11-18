<script setup lang="ts">
// Stub. Task 16 replaces this file in full (workspaces, folders, status,
// add folder, add workspace). App.vue renders it inside <nav class="side">.
import { store } from '../stores/state'
</script>

<template>
  <div class="side-stub">
    <h2 class="side-title">Workspaces</h2>
    <ul v-if="store.state.data?.workspaces.length" class="ws-list">
      <li v-for="ws in store.state.data.workspaces" :key="ws.name">
        <span class="name">{{ ws.name }}</span>
        <span class="c-muted">{{ ws.folders.length }} folders</span>
      </li>
    </ul>
    <p v-else class="empty">No workspaces yet.</p>
  </div>
</template>

<style scoped>
.side-title { padding: 0 var(--s-5) var(--s-3); color: var(--text-muted); font-weight: 400; }
.ws-list { list-style: none; margin: 0; padding: 0; }
.ws-list li { display: flex; justify-content: space-between; gap: var(--s-3); min-height: var(--row-h); align-items: center; padding: 0 var(--s-5); }
.name { font-weight: 600; }
</style>
