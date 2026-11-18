<script setup lang="ts">
// Shown when the tab has no UI token, or the daemon refused it (the token
// changes on every start). Never shows the raw server reply.
import Icon from './Icon.vue'

const origin = window.location.origin
</script>

<template>
  <main class="notoken">
    <section class="card" aria-labelledby="notoken-h">
      <div class="brand"><Icon name="term" />relaydeck</div>
      <h1 id="notoken-h">Open relaydeck from the link printed by <code>relaydeck serve</code>.</h1>
      <p class="c-muted">
        The link starts with <code>{{ origin }}/#token=</code> and ends with a long code. That code changes
        every time relaydeck starts, so an old link or a bookmark stops working.
      </p>
    </section>
  </main>
</template>

<style scoped>
.notoken { min-height: 100vh; display: grid; place-items: center; padding: var(--s-8); }
.card {
  width: 100%; max-width: 560px; display: grid; gap: var(--s-5);
  padding: var(--s-8); background: var(--surface); border: 1px solid var(--border); border-radius: var(--r);
}
.brand { display: flex; align-items: center; gap: var(--s-4); font-weight: 700; }
.brand .i { width: 16px; height: 16px; color: var(--green); }
h1 { font-size: var(--fs-lg); font-weight: 700; line-height: 1.5; }
p { margin: 0; overflow-wrap: anywhere; }
</style>
