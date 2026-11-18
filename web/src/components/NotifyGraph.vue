<script setup lang="ts">
// Who messaged whom in the active workspace, as inline SVG: one node per
// folder, one curved arrow per from/to pair seen in the timeline. The line
// style carries the meaning (solid, dashed, dotted), the color repeats it.
// The timeline next to it is the readable source of the same data.
//
// The SVG is drawn at scale 1 (one unit is one pixel), as wide as its box,
// so its text is never smaller than 14px. When it does not fit, the box
// scrolls instead of shrinking the drawing.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { clock, type Messages } from '../stores/messages'
import type { AgentStatus, FolderState, Message, MessageState } from '../types'
import Icon, { type IconName } from './Icon.vue'

const props = defineProps<{ messages: Messages; folders: FolderState[]; workspace: string }>()

const statusMeta: Record<AgentStatus, { icon: IconName; cls: string; word: string }> = {
  busy: { icon: 'busy', cls: 'c-busy', word: 'busy' },
  idle: { icon: 'idle', cls: 'c-idle', word: 'idle' },
  attention: { icon: 'attn', cls: 'c-attn', word: 'needs attention' },
  stopped: { icon: 'stop', cls: 'c-stop', word: 'stopped' },
  starting: { icon: 'start', cls: 'c-start', word: 'starting' },
}

// Layout in px (scale 1). JetBrains Mono is 0.6 em per character.
const FS = 14
const CHAR = FS * 0.6
const TEXT_X = 28 // left padding 8, icon 14, gap 6
const PAD_R = 10
const NH = 28
const TOP = 8
const SIDE = 4
const MIN_GAP = 44 // between the two columns, room for an arrow
const maxName = 11

// The width of the box the graph sits in; ResizeObserver keeps it current.
const root = ref<HTMLElement | null>(null)
const boxW = ref(324)
let ro: ResizeObserver | null = null
onMounted(() => {
  if (typeof ResizeObserver === 'undefined' || !root.value) return
  ro = new ResizeObserver(([e]) => {
    if (e) boxW.value = Math.floor(e.contentRect.width)
  })
  ro.observe(root.value)
})
onUnmounted(() => ro?.disconnect())

const cut = (name: string) => {
  const chars = Array.from(name)
  return chars.length > maxName ? chars.slice(0, maxName - 1).join('') + '…' : name
}

interface Node { name: string; x: number; y: number; cx: number; cy: number; label: string; title: string; icon: IconName; cls: string }

const nodeW = computed(() => Math.max(118, Math.ceil(TEXT_X + Math.max(0, ...props.folders.map((f) => Array.from(cut(f.name)).length)) * CHAR + PAD_R)))
const W = computed(() => Math.max(2 * nodeW.value + 2 * SIDE + MIN_GAP, boxW.value))

const nodes = computed<Node[]>(() => {
  const rows = Math.ceil(props.folders.length / 2)
  const pitch = rows <= 2 ? 80 : 64
  const nw = nodeW.value
  return props.folders.map((f, i) => {
    const x = i % 2 === 0 ? SIDE : W.value - SIDE - nw
    const y = TOP + Math.floor(i / 2) * pitch
    const meta = statusMeta[f.status] ?? statusMeta.stopped
    return { name: f.name, x, y, cx: x + nw / 2, cy: y + NH / 2, label: cut(f.name), title: `${f.name}: ${meta.word}`, icon: meta.icon, cls: meta.cls }
  })
})

const H = computed(() => {
  const last = nodes.value[nodes.value.length - 1]
  return last ? last.y + NH + TOP : 0
})

type Style = 'solid' | 'dashed' | 'dotted'

// A rejected message was never delivered, so it draws nothing. rank: the
// pair shows the state that needs the user most.
const styleOf: Partial<Record<MessageState, { style: Style; rank: number }>> = {
  awaiting_approval: { style: 'dotted', rank: 2 },
  held_external: { style: 'dashed', rank: 1 },
  held_draft: { style: 'dashed', rank: 1 },
  queued: { style: 'dashed', rank: 1 },
  nudged: { style: 'dashed', rank: 1 },
  delivered: { style: 'solid', rank: 0 },
}

// JSON keeps the two names apart whatever characters they contain.
const pairKey = (from: string, to: string) => JSON.stringify([from, to])

function drawable(m: Message): boolean {
  return styleOf[m.state] !== undefined && m.from !== m.to && props.folders.some((f) => f.name === m.from) && props.folders.some((f) => f.name === m.to)
}

const latestMsg = computed(() => props.messages.newestFirst.value.find(drawable) ?? null)

interface Edge { key: string; from: string; to: string; d: string; style: Style; lit: boolean; latest: boolean; mx: number; my: number; nx: number; ny: number }

const edges = computed<Edge[]>(() => {
  const byName = new Map(nodes.value.map((nd) => [nd.name, nd]))
  const pairs = new Map<string, { from: Node; to: Node; rank: number; style: Style; lit: boolean }>()
  const latest = latestMsg.value ? pairKey(latestMsg.value.from, latestMsg.value.to) : ''
  // creation order, so the first pair seen is drawn first
  for (const m of [...props.messages.newestFirst.value].reverse()) {
    if (!drawable(m)) continue
    const s = styleOf[m.state]!
    const key = pairKey(m.from, m.to)
    const p = pairs.get(key) ?? { from: byName.get(m.from)!, to: byName.get(m.to)!, rank: -1, style: 'solid' as Style, lit: false }
    if (s.rank > p.rank) {
      p.rank = s.rank
      p.style = s.style
    }
    if (props.messages.lit.has(m.id)) p.lit = true
    pairs.set(key, p)
  }
  const out: Edge[] = []
  for (const [key, p] of pairs) out.push({ key, from: p.from.name, to: p.to.name, style: p.style, lit: p.lit, latest: key === latest, ...curve(p.from, p.to) })
  // green edges on top
  return out.sort((a, b) => Number(a.lit || a.latest) - Number(b.lit || b.latest))
})

// curve bends each arrow a little to the right of its direction, so a->b and
// b->a never overlap and a long arrow does not run through a middle node.
// It returns the curve's middle point and the unit normal toward the bend.
function curve(a: Node, b: Node) {
  const dx = b.cx - a.cx
  const dy = b.cy - a.cy
  const len = Math.hypot(dx, dy) || 1
  const nx = -dy / len
  const ny = dx / len
  const bend = Math.min(24, len * 0.15)
  const qx = (a.cx + b.cx) / 2 + nx * bend
  const qy = (a.cy + b.cy) / 2 + ny * bend
  const s = exit(a, qx, qy)
  const e = exit(b, qx, qy)
  return { d: `M${r1(s.x)} ${r1(s.y)}Q${r1(qx)} ${r1(qy)} ${r1(e.x)} ${r1(e.y)}`, mx: (s.x + 2 * qx + e.x) / 4, my: (s.y + 2 * qy + e.y) / 4, nx, ny }
}

const r1 = (n: number) => Math.round(n * 10) / 10

// exit is where the line from the node center toward (tx, ty) leaves the
// node box, plus a small gap.
function exit(n: Node, tx: number, ty: number): { x: number; y: number } {
  const gap = 3
  const dx = tx - n.cx
  const dy = ty - n.cy
  const t = Math.min(dx === 0 ? Infinity : (nodeW.value / 2 + gap) / Math.abs(dx), dy === 0 ? Infinity : (NH / 2 + gap) / Math.abs(dy))
  return { x: n.cx + dx * t, y: n.cy + dy * t }
}

// The latest edge is labelled with its time only (the legend says "latest").
// The label sits beside the curve, on the first side and distance where it
// stays inside the drawing and keeps 4px clear of every node.
const latestLabel = computed(() => {
  const m = latestMsg.value
  const e = edges.value.find((x) => x.latest)
  if (!m || !e) return null
  const text = clock(m.created, false)
  const lw = text.length * CHAR
  const fits = (x: number, y: number, anchor: string) => {
    const left = anchor === 'middle' ? x - lw / 2 : anchor === 'end' ? x - lw : x
    const b = { x1: left, x2: left + lw, y1: y - FS / 2, y2: y + FS / 2 }
    if (b.x1 < 0 || b.x2 > W.value || b.y1 < 0 || b.y2 > H.value) return false
    return nodes.value.every((nd) => b.x2 < nd.x - 4 || b.x1 > nd.x + nodeW.value + 4 || b.y2 < nd.y - 4 || b.y1 > nd.y + NH + 4)
  }
  let first: { x: number; y: number; anchor: string } | null = null
  for (const dist of [14, 24, 36]) {
    for (const side of [1, -1]) {
      const nx = e.nx * side
      const ny = e.ny * side
      const anchor = Math.abs(nx) > 0.7 ? (nx > 0 ? 'start' : 'end') : 'middle'
      const c = { x: r1(e.mx + nx * dist), y: r1(e.my + ny * dist), anchor }
      first ??= c
      if (fits(c.x, c.y, c.anchor)) return { ...c, text, from: m.from, to: m.to }
    }
  }
  return { ...first!, text, from: m.from, to: m.to }
})

const summary = computed(() => {
  const base = `Message graph of ${props.workspace}: ${nodes.value.length} folders, ${edges.value.length} routes`
  const m = latestMsg.value
  return m ? `${base}, latest ${m.from} to ${m.to}` : base
})

const marker = (e: Edge) => (e.lit || e.latest ? 'green' : e.style === 'solid' ? 'solid' : 'held')
</script>

<template>
  <div ref="root" class="gr">
    <div class="gr-h"><span>graph &middot; {{ workspace }}</span></div>
    <p v-if="nodes.length === 0" class="none">No folders in this workspace.</p>
    <svg v-else class="graph" :width="W" :height="H" :viewBox="`0 0 ${W} ${H}`" role="img" :aria-label="summary">
      <defs>
        <marker id="rd-arrow-solid" class="mk solid" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" /></marker>
        <marker id="rd-arrow-held" class="mk held" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" /></marker>
        <marker id="rd-arrow-green" class="mk green" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" /></marker>
      </defs>
      <path
        v-for="e in edges"
        :key="e.key"
        class="edge"
        :class="[e.style, { lit: e.lit, latest: e.latest }]"
        :data-from="e.from"
        :data-to="e.to"
        :d="e.d"
        :marker-end="`url(#rd-arrow-${marker(e)})`"
      />
      <g v-for="nd in nodes" :key="nd.name" class="node" :class="nd.cls">
        <title>{{ nd.title }}</title>
        <rect :x="nd.x" :y="nd.y" :width="nodeW" :height="NH" rx="5" />
        <Icon :name="nd.icon" :x="nd.x + 8" :y="nd.y + 7" width="14" height="14" />
        <text :x="nd.x + TEXT_X" :y="nd.y + 19" :font-size="FS">{{ nd.label }}</text>
      </g>
      <text
        v-if="latestLabel"
        class="when"
        :data-from="latestLabel.from"
        :data-to="latestLabel.to"
        :x="latestLabel.x"
        :y="latestLabel.y"
        :text-anchor="latestLabel.anchor"
        dominant-baseline="middle"
        :font-size="FS"
      >{{ latestLabel.text }}</text>
    </svg>
    <div class="gl">
      <span><svg width="22" height="8" aria-hidden="true"><line class="edge solid latest" x1="0" y1="4" x2="22" y2="4" /></svg>latest</span>
      <span><svg width="22" height="8" aria-hidden="true"><line class="edge solid" x1="0" y1="4" x2="22" y2="4" /></svg>delivered</span>
      <span title="queued, nudged, waiting or held"><svg width="22" height="8" aria-hidden="true"><line class="edge dashed" x1="0" y1="4" x2="22" y2="4" /></svg>held</span>
      <span title="awaiting approval"><svg width="22" height="8" aria-hidden="true"><line class="edge dotted" x1="0" y1="4" x2="22" y2="4" /></svg>approval</span>
    </div>
  </div>
</template>

<style scoped>
.gr { padding: var(--s-1) var(--s-5) var(--s-3); display: flex; flex-direction: column; gap: var(--s-1); min-width: 0; overflow: auto; }
.gr-h { color: var(--text-muted); }
.none { margin: 0; color: var(--text-muted); }
.graph { display: block; flex: none; }

.edge { fill: none; stroke: var(--text-muted); stroke-width: 1.5; }
.edge.dashed { stroke: var(--amber); stroke-dasharray: 5 4; }
.edge.dotted { stroke: var(--amber); stroke-dasharray: 2 3; }
/* the latest edge and an edge that just changed (2 s) */
.edge.latest, .edge.lit { stroke: var(--green); stroke-width: 2.5; }
.mk path { fill: var(--text-muted); }
.mk.held path { fill: var(--amber); }
.mk.green path { fill: var(--green); }
@media (prefers-reduced-motion: no-preference) {
  .edge { transition: stroke .3s ease-out, stroke-width .3s ease-out; }
}

.when { fill: var(--green); paint-order: stroke; stroke: var(--surface); stroke-width: 4px; stroke-linejoin: round; }
.node rect { fill: var(--muted); stroke: currentColor; }
.node text { fill: var(--text); }

.gl { display: flex; flex-wrap: wrap; gap: var(--s-1) var(--s-6); color: var(--text-muted); }
.gl span { display: inline-flex; align-items: center; gap: 5px; }

@media (max-width: 767px) {
  .gr { padding: var(--s-4) var(--gutter-mobile) var(--s-8); }
}
</style>
