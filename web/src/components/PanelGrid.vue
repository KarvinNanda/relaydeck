<script setup lang="ts">
// Stub. Task 17 replaces this file in full (terminal panels and the layout
// switcher). The layout switcher goes into the top bar with
// <Teleport defer to="#layout-slot">, so App.vue does not change.
</script>

<template>
  <div class="panels-empty">
    <p class="title">No terminal open</p>
    <p class="c-muted">Start an agent from a folder in the sidebar.</p>
  </div>
</template>

<style scoped>
.panels-empty {
  height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--s-2); padding: var(--s-8); text-align: center;
}
.panels-empty p { margin: 0; }
.title { font-weight: 700; font-size: var(--fs-lg); }
@media (max-width: 767px) {
  .panels-empty { padding: var(--s-8) var(--gutter-mobile); }
}
</style>
