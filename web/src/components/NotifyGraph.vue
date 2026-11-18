<script setup lang="ts">
// Who messaged whom in the active workspace, as inline SVG: one node per
// folder, one curved arrow per from/to pair seen in the timeline. The line
// style carries the meaning (solid, dashed, dotted), the color repeats it.
// The timeline next to it is the readable source of the same data.
import { computed } from 'vue'
import { clock, type Messages } from '../stores/messages'
import type { AgentStatus, FolderState, MessageState } from '../types'
import Icon, { type IconName } from './Icon.vue'

const props = defineProps<{ messages: Messages; folders: FolderState[]; workspace: string }>()

const statusMeta: Record<AgentStatus, { icon: IconName; cls: string; word: string }> = {
  busy: { icon: 'busy', cls: 'c-busy', word: 'busy' },
  idle: { icon: 'idle', cls: 'c-idle', word: 'idle' },
  attention: { icon: 'attn', cls: 'c-attn', word: 'needs attention' },
  stopped: { icon: 'stop', cls: 'c-stop', word: 'stopped' },
  starting: { icon: 'start', cls: 'c-start', word: 'starting' },
}

// Layout in viewBox units: two columns of nodes, as in mockup A.
const W = 340
const NW = 118
const NH = 28
const TOP = 12
const maxName = 11

interface Node { name: string; x: number; y: number; cx: number; cy: number; label: string; title: string; icon: IconName; cls: string }

const nodes = computed<Node[]>(() => {
  const rows = Math.ceil(props.folders.length / 2)
  const pitch = rows <= 2 ? 98 : 64
  return props.folders.map((f, i) => {
    const x = i % 2 === 0 ? 8 : W - 8 - NW
    const y = TOP + Math.floor(i / 2) * pitch
    const meta = statusMeta[f.status] ?? statusMeta.stopped
    const label = Array.from(f.name).length > maxName ? Array.from(f.name).slice(0, maxName - 1).join('') + '…' : f.name
    return { name: f.name, x, y, cx: x + NW / 2, cy: y + NH / 2, label, title: `${f.name}: ${meta.word}`, icon: meta.icon, cls: meta.cls }
  })
})

const height = computed(() => {
  const last = nodes.value[nodes.value.length - 1]
  return last ? last.y + NH + 12 : 0
})

type Style = 'solid' | 'dashed' | 'dotted'

// rank: the pair shows the state that needs the user most.
const styleOf: Record<MessageState, { style: Style; rank: number }> = {
  awaiting_approval: { style: 'dotted', rank: 2 },
  held_external: { style: 'dashed', rank: 1 },
  held_draft: { style: 'dashed', rank: 1 },
  queued: { style: 'dashed', rank: 1 },
  nudged: { style: 'dashed', rank: 1 },
  delivered: { style: 'solid', rank: 0 },
  rejected: { style: 'solid', rank: 0 },
}

interface Edge { key: string; d: string; style: Style; lit: boolean; lx: number; ly: number }

const edges = computed<Edge[]>(() => {
  const byName = new Map(nodes.value.map((nd) => [nd.name, nd]))
  const pairs = new Map<string, { from: Node; to: Node; rank: number; style: Style; lit: boolean }>()
  // creation order, so the first pair seen is drawn first
  const list = [...props.messages.newestFirst.value].reverse()
  for (const m of list) {
    const from = byName.get(m.from)
    const to = byName.get(m.to)
    if (!from || !to || from === to) continue
    const key = `${m.from}>${m.to}`
    const s = styleOf[m.state] ?? { style: 'solid' as Style, rank: 0 }
    const p = pairs.get(key) ?? { from, to, rank: -1, style: 'solid' as Style, lit: false }
    if (s.rank > p.rank) {
      p.rank = s.rank
      p.style = s.style
    }
    if (props.messages.lit.has(m.id)) p.lit = true
    pairs.set(key, p)
  }
  const out: Edge[] = []
  for (const [key, p] of pairs) out.push({ key, style: p.style, lit: p.lit, ...curve(p.from, p.to) })
  // lit edges on top
  return out.sort((a, b) => Number(a.lit) - Number(b.lit))
})

// curve bends each arrow a little to the right of its direction, so a->b and
// b->a never overlap and a long arrow does not run through a middle node.
function curve(a: Node, b: Node): { d: string; lx: number; ly: number } {
  const dx = b.cx - a.cx
  const dy = b.cy - a.cy
  const len = Math.hypot(dx, dy) || 1
  const bend = Math.min(24, len * 0.15)
  const qx = (a.cx + b.cx) / 2 - (dy / len) * bend
  const qy = (a.cy + b.cy) / 2 + (dx / len) * bend
  const s = exit(a, qx, qy)
  const e = exit(b, qx, qy)
  const r = (n: number) => Math.round(n * 10) / 10
  return { d: `M${r(s.x)} ${r(s.y)}Q${r(qx)} ${r(qy)} ${r(e.x)} ${r(e.y)}`, lx: r((s.x + 2 * qx + e.x) / 4), ly: r((s.y + 2 * qy + e.y) / 4) }
}

// exit is where the line from the node center toward (tx, ty) leaves the
// node box, plus a small gap.
function exit(n: Node, tx: number, ty: number): { x: number; y: number } {
  const gap = 3
  const dx = tx - n.cx
  const dy = ty - n.cy
  const t = Math.min(dx === 0 ? Infinity : (NW / 2 + gap) / Math.abs(dx), dy === 0 ? Infinity : (NH / 2 + gap) / Math.abs(dy))
  return { x: n.cx + dx * t, y: n.cy + dy * t }
}

const latest = computed(() => {
  const m = props.messages.newestFirst.value.find((x) => edges.value.some((e) => e.key === `${x.from}>${x.to}`))
  if (!m) return null
  const e = edges.value.find((x) => x.key === `${m.from}>${m.to}`)!
  return { key: e.key, x: e.lx, y: e.ly - 6, text: `${clock(m.created, false)} latest`, from: m.from, to: m.to }
})

const summary = computed(() => {
  const base = `Message graph of ${props.workspace}: ${nodes.value.length} folders, ${edges.value.length} routes`
  return latest.value ? `${base}, latest ${latest.value.from} to ${latest.value.to}` : base
})
</script>

<template>
  <div class="gr">
    <div class="gr-h"><span>graph &middot; {{ workspace }}</span></div>
    <p v-if="nodes.length === 0" class="none">No folders in this workspace.</p>
    <svg v-else class="graph" :viewBox="`0 0 ${W} ${height}`" role="img" :aria-label="summary">
      <defs>
        <marker id="rd-arrow-solid" class="mk solid" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" /></marker>
        <marker id="rd-arrow-held" class="mk held" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" /></marker>
        <marker id="rd-arrow-lit" class="mk lit" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" /></marker>
      </defs>
      <path
        v-for="e in edges"
        :key="e.key"
        class="edge"
        :class="[e.style, { lit: e.lit }]"
        :data-edge="e.key"
        :d="e.d"
        :marker-end="`url(#rd-arrow-${e.lit ? 'lit' : e.style === 'solid' ? 'solid' : 'held'})`"
      />
      <text v-if="latest" class="when" :data-for="latest.key" :x="latest.x" :y="latest.y" text-anchor="middle">{{ latest.text }}</text>
      <g v-for="nd in nodes" :key="nd.name" class="node" :class="nd.cls">
        <title>{{ nd.title }}</title>
        <rect :x="nd.x" :y="nd.y" :width="NW" :height="NH" rx="5" />
        <Icon :name="nd.icon" :x="nd.x + 8" :y="nd.y + 7" width="14" height="14" />
        <text :x="nd.x + 28" :y="nd.y + 19">{{ nd.label }}</text>
      </g>
    </svg>
    <div class="gl">
      <span><svg width="22" height="8" aria-hidden="true"><line class="edge solid lit" x1="0" y1="4" x2="22" y2="4" /></svg>new (2 s)</span>
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
/* The graph takes the height left in the drawer and scales to fit it. */
.graph { flex: 1 1 0; min-height: 96px; width: 100%; display: block; }

.edge { fill: none; stroke: var(--text-muted); stroke-width: 1.5; }
.edge.dashed { stroke: var(--amber); stroke-dasharray: 5 4; }
.edge.dotted { stroke: var(--amber); stroke-dasharray: 2 3; }
.edge.lit { stroke: var(--green); stroke-width: 2.5; }
.mk path { fill: var(--text-muted); }
.mk.held path { fill: var(--amber); }
.mk.lit path { fill: var(--green); }
@media (prefers-reduced-motion: no-preference) {
  .edge { transition: stroke .3s ease-out, stroke-width .3s ease-out; }
}

.when { fill: var(--text); font-size: 12px; paint-order: stroke; stroke: var(--surface); stroke-width: 4px; stroke-linejoin: round; }
.node rect { fill: var(--muted); stroke: currentColor; }
.node text { fill: var(--text); font-size: 13px; }

.gl { display: flex; flex-wrap: wrap; gap: var(--s-1) var(--s-6); color: var(--text-muted); }
.gl span { display: inline-flex; align-items: center; gap: 5px; }

@media (max-width: 767px) {
  .gr { padding: var(--s-4) var(--gutter-mobile) var(--s-8); overflow: visible; }
  .graph { flex: none; height: auto; min-height: 0; }
}
</style>
