<script setup lang="ts">
// The layout switcher of mockup A, in the top bar. PanelGrid owns the
// layout; this only shows it and reports a pick. It renders into
// App.vue's #layout-slot. The Teleport must be deferred: .app mounts after
// its children, so the slot does not exist yet when this component mounts.
import { computed } from 'vue'

const props = defineProps<{ count: number; layout: number }>()
const emit = defineEmits<{ pick: [n: number] }>()

// Icon paths draw the layout itself (18x14 frame, as in mockup A).
const choices = [
  { n: 1, label: '1 panel, full', lines: '' },
  { n: 2, label: '2 panels, side by side', lines: 'M9 1v12' },
  { n: 3, label: '3 panels, left full height, right split', lines: 'M9 1v12M9 7h8' },
  { n: 4, label: '4 panels, 2 by 2', lines: 'M9 1v12M1 7h16' },
]

function plural(n: number): string {
  return `${n} ${n === 1 ? 'panel' : 'panels'}`
}

// Phones stack every shown panel, so they only get the count.
const countText = computed(() =>
  props.layout < props.count ? `${plural(props.count)} open, ${props.layout} shown` : `${plural(props.count)} open`,
)
</script>

<template>
  <Teleport defer to="#layout-slot">
    <div class="layouts" role="group" aria-label="Layout">
      <span class="layouts-label">Layout</span>
      <button
        v-for="c in choices"
        :key="c.n"
        class="lay"
        type="button"
        :aria-pressed="c.n === layout ? 'true' : 'false'"
        :aria-label="c.label"
        :title="c.n > count ? `${c.label} (open ${plural(c.n)} first)` : c.label"
        :disabled="c.n > count"
        @click="emit('pick', c.n)"
      >
        <svg viewBox="0 0 18 14" aria-hidden="true" focusable="false">
          <rect x=".75" y=".75" width="16.5" height="12.5" rx="1.5" />
          <path v-if="c.lines" :d="c.lines" />
        </svg>
      </button>
    </div>
    <span class="count-only">{{ countText }}</span>
  </Teleport>
</template>

<style scoped>
.layouts { display: flex; align-items: center; gap: var(--s-2); }
.layouts-label { color: var(--text-muted); margin-right: var(--s-2); }
.lay {
  width: 32px; height: var(--control-h); display: grid; place-items: center; padding: 0;
  border: 1px solid var(--border); border-radius: var(--r); background: var(--bg); cursor: pointer;
  color: var(--text-muted);
}
.lay svg { width: 18px; height: 14px; fill: none; stroke: currentColor; stroke-width: 1.5; }
.lay:hover:not(:disabled) { border-color: var(--text-muted); color: var(--text); }
.lay[aria-pressed="true"] { border-color: var(--green); color: var(--green); background: var(--green-tint); }
.lay:disabled { opacity: .45; cursor: default; }
.count-only { display: none; color: var(--text-muted); }

/* phone: panels stack in one column, so only the count is shown */
@media (max-width: 767px) {
  .layouts { display: none; }
  .count-only { display: inline; }
}
</style>
