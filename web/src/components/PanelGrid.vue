<script lang="ts">
import type { PanelKey } from '../stores/panels'

// Grid templates for each layout (spec 7.8). Cell i gets area "abcd"[i].
export const templateAreas: Record<number, string> = {
  1: '"a"',
  2: '"a b"',
  3: '"a b" "a c"', // left full height, right split in two
  4: '"a b" "c d"', // 2x2
}

export const areas = ['a', 'b', 'c', 'd']

// visiblePanels picks the n panels to show: the first n in open order, but
// the focused panel is always one of them (it takes the last cell).
export function visiblePanels(list: readonly PanelKey[], focused: PanelKey | null, n: number): PanelKey[] {
  const first = list.slice(0, n)
  if (!focused || first.some((p) => same(p, focused))) return first
  const f = list.find((p) => same(p, focused))
  return f ? [...list.slice(0, n - 1), f] : first
}

export function same(a: PanelKey, b: PanelKey): boolean {
  return a.ws === b.ws && a.folder === b.folder
}
</script>

<script setup lang="ts">
// The terminal panels: 1 to 4 in the layouts of spec 7.8, and the layout
// switcher in the top bar. The layout follows the panel count; the user can
// pick a smaller one (or maximize a panel) to see fewer panels. Hidden
// panels are unmounted: their agents keep running, and showing them again
// reconnects with a fresh replay.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { panelStore } from '../stores/panels'
import TerminalPanel from './TerminalPanel.vue'
import LayoutSwitcher from './LayoutSwitcher.vue'

// null: automatic, one cell per open panel.
const chosen = ref<number | null>(null)

// Phones stack every open panel in one column, whatever layout is picked
// (ruling): there is nothing to hide, so the layout is "all of them".
const phone = ref(false)
const mq = typeof window !== 'undefined' ? (window.matchMedia?.('(max-width: 767px)') ?? null) : null
if (mq) {
  phone.value = mq.matches
  const onChange = (e: MediaQueryListEvent) => (phone.value = e.matches)
  mq.addEventListener?.('change', onChange)
  onBeforeUnmount(() => mq.removeEventListener?.('change', onChange))
}

const count = computed(() => panelStore.panels.length)
const layout = computed(() => (phone.value ? count.value : Math.min(chosen.value ?? count.value, count.value)))
const shown = computed(() => visiblePanels(panelStore.panels, panelStore.focused, layout.value))

// A panel opened or closed: go back to showing all of them.
watch(count, () => {
  chosen.value = null
})

function isFocused(p: PanelKey): boolean {
  const f = panelStore.focused
  return f !== null && same(p, f)
}

function maximize(p: PanelKey): void {
  panelStore.focusPanel(p.ws, p.folder)
  chosen.value = 1
}
</script>

<template>
  <div v-if="count === 0" class="panels-empty">
    <p class="title">No terminal open</p>
    <p class="c-muted">Pick a folder under Workspaces to start an agent.</p>
  </div>
  <template v-else>
    <LayoutSwitcher :count="count" :layout="layout" @pick="chosen = $event" />
    <div class="grid" :class="`n${layout}`" :style="{ '--areas': templateAreas[layout] }">
      <div v-for="(p, i) in shown" :key="`${p.ws}\n${p.folder}`" class="cell" :data-area="areas[i]">
        <TerminalPanel
          :ws="p.ws"
          :folder="p.folder"
          :focused="isFocused(p)"
          :maximized="layout === 1 && count > 1"
          :can-maximize="count > 1 && !phone"
          @maximize="maximize(p)"
          @restore="chosen = null"
        />
      </div>
    </div>
  </template>
</template>

<style scoped>
.panels-empty {
  height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: var(--s-2); padding: var(--s-8); text-align: center;
}
.panels-empty p { margin: 0; }
.title { font-weight: 700; font-size: var(--fs-lg); }

/* The areas come from templateAreas through --areas (inline style on .grid),
   so the test covers what is rendered. */
.grid {
  height: 100%; display: grid; gap: var(--s-3); padding: var(--s-3);
  grid-template-areas: var(--areas);
  grid-template-columns: minmax(0, 1fr);
  grid-template-rows: minmax(0, 1fr);
}
.grid.n2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.grid.n3, .grid.n4 { grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: repeat(2, minmax(0, 1fr)); }
.cell { min-width: 0; min-height: 0; display: flex; }
.cell[data-area="a"] { grid-area: a; }
.cell[data-area="b"] { grid-area: b; }
.cell[data-area="c"] { grid-area: c; }
.cell[data-area="d"] { grid-area: d; }

/* phone: one column, every shown panel stacked in open order */
@media (max-width: 767px) {
  .panels-empty { padding: var(--s-8) var(--gutter-mobile); }
  .grid.n1, .grid.n2, .grid.n3, .grid.n4 {
    height: auto; grid-template-areas: none; grid-template-columns: minmax(0, 1fr); grid-template-rows: none;
    padding: var(--s-5) var(--gutter-mobile); gap: var(--s-5);
  }
  .cell[data-area] { grid-area: auto; }
}
</style>
