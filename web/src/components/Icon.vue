<script setup lang="ts">
// Lucide-style line icons from mockup A, drawn as inline SVG (never emoji).
// Decorative by default (aria-hidden): the text next to the icon carries the
// meaning. Pass label for an icon that stands alone.
import { computed } from 'vue'

type Shape =
  | { tag: 'path'; d: string }
  | { tag: 'circle'; cx: number; cy: number; r: number; fill?: boolean }
  | { tag: 'rect'; x: number; y: number; width: number; height: number; rx: number }

const icons = {
  term: [
    { tag: 'path', d: 'm7 11 2-2-2-2' },
    { tag: 'path', d: 'M11 13h4' },
    { tag: 'rect', x: 3, y: 3, width: 18, height: 18, rx: 2 },
  ],
  busy: [{ tag: 'path', d: 'M21 12a9 9 0 1 1-6.219-8.56' }],
  idle: [
    { tag: 'circle', cx: 12, cy: 12, r: 9 },
    { tag: 'path', d: 'M10 15V9' },
    { tag: 'path', d: 'M14 15V9' },
  ],
  attn: [
    { tag: 'path', d: 'm21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3' },
    { tag: 'path', d: 'M12 9v4' },
    { tag: 'path', d: 'M12 17h.01' },
  ],
  stop: [{ tag: 'circle', cx: 12, cy: 12, r: 6, fill: true }],
  start: [
    { tag: 'path', d: 'M5 22h14' },
    { tag: 'path', d: 'M5 2h14' },
    { tag: 'path', d: 'M17 22v-4.172a2 2 0 0 0-.586-1.414L12 12l-4.414 4.414A2 2 0 0 0 7 17.828V22' },
    { tag: 'path', d: 'M7 2v4.172a2 2 0 0 0 .586 1.414L12 12l4.414-4.414A2 2 0 0 0 17 6.172V2' },
  ],
  history: [
    { tag: 'path', d: 'M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8' },
    { tag: 'path', d: 'M3 3v5h5' },
    { tag: 'path', d: 'M12 7v5l4 2' },
  ],
  square: [{ tag: 'rect', x: 5, y: 5, width: 14, height: 14, rx: 2 }],
  max: [
    { tag: 'path', d: 'M15 3h6v6' },
    { tag: 'path', d: 'M9 21H3v-6' },
    { tag: 'path', d: 'M21 3l-7 7' },
    { tag: 'path', d: 'M3 21l7-7' },
  ],
  restore: [
    { tag: 'path', d: 'm14 10 7-7' },
    { tag: 'path', d: 'M20 10h-6V4' },
    { tag: 'path', d: 'm3 21 7-7' },
    { tag: 'path', d: 'M4 14h6v6' },
  ],
  down: [{ tag: 'path', d: 'm6 9 6 6 6-6' }],
  up: [{ tag: 'path', d: 'm18 15-6-6-6 6' }],
  right: [{ tag: 'path', d: 'm9 18 6-6-6-6' }],
  layers: [
    { tag: 'path', d: 'm12 2 9 5-9 5-9-5 9-5Z' },
    { tag: 'path', d: 'm3 12 9 5 9-5' },
    { tag: 'path', d: 'm3 17 9 5 9-5' },
  ],
  plus: [
    { tag: 'path', d: 'M5 12h14' },
    { tag: 'path', d: 'M12 5v14' },
  ],
  fplus: [
    { tag: 'path', d: 'M12 10v6' },
    { tag: 'path', d: 'M9 13h6' },
    { tag: 'path', d: 'M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z' },
  ],
  msg: [{ tag: 'path', d: 'M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z' }],
  checks: [
    { tag: 'path', d: 'M18 6 7 17l-5-5' },
    { tag: 'path', d: 'm22 10-7.5 7.5L13 16' },
  ],
  check: [{ tag: 'path', d: 'M20 6 9 17l-5-5' }],
  clock: [
    { tag: 'circle', cx: 12, cy: 12, r: 9 },
    { tag: 'path', d: 'M12 7v5l3 2' },
  ],
  pause: [
    { tag: 'rect', x: 14, y: 4, width: 4, height: 16, rx: 1 },
    { tag: 'rect', x: 6, y: 4, width: 4, height: 16, rx: 1 },
  ],
  shield: [
    { tag: 'path', d: 'M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z' },
    { tag: 'path', d: 'M12 8v4' },
    { tag: 'path', d: 'M12 16h.01' },
  ],
  user: [
    { tag: 'circle', cx: 12, cy: 8, r: 4 },
    { tag: 'path', d: 'M20 21a8 8 0 0 0-16 0' },
  ],
  play: [{ tag: 'path', d: 'M6 3l14 9-14 9V3z' }],
  x: [
    { tag: 'path', d: 'M18 6 6 18' },
    { tag: 'path', d: 'm6 6 12 12' },
  ],
  hash: [
    { tag: 'path', d: 'M4 9h16' },
    { tag: 'path', d: 'M4 15h16' },
    { tag: 'path', d: 'M10 3 8 21' },
    { tag: 'path', d: 'M16 3l-2 18' },
  ],
} satisfies Record<string, Shape[]>

export type IconName = keyof typeof icons

const props = defineProps<{ name: IconName; label?: string }>()
const shapes = computed<Shape[]>(() => icons[props.name])
</script>

<template>
  <svg
    class="i"
    viewBox="0 0 24 24"
    :role="label ? 'img' : undefined"
    :aria-label="label"
    :aria-hidden="label ? undefined : 'true'"
    focusable="false"
  >
    <template v-for="(s, n) in shapes" :key="n">
      <path v-if="s.tag === 'path'" :d="s.d" />
      <circle v-else-if="s.tag === 'circle'" :cx="s.cx" :cy="s.cy" :r="s.r" :class="{ fill: s.fill }" />
      <rect v-else :x="s.x" :y="s.y" :width="s.width" :height="s.height" :rx="s.rx" />
    </template>
  </svg>
</template>

<style scoped>
.fill { fill: currentColor; stroke: none; }
</style>
