<script lang="ts">
import type { AgentStatus } from '../types'
import type { IconName } from './Icon.vue'

export interface StatusInfo {
  word: string // the text shown to the user
  cls: string // color class from base.css
  icon: IconName
}

const info: Record<AgentStatus, StatusInfo> = {
  busy: { word: 'busy', cls: 'c-busy', icon: 'busy' },
  idle: { word: 'idle', cls: 'c-idle', icon: 'idle' },
  attention: { word: 'needs attention', cls: 'c-attn', icon: 'attn' },
  stopped: { word: 'stopped', cls: 'c-stop', icon: 'stop' },
  starting: { word: 'starting', cls: 'c-start', icon: 'start' },
}

// The order of the status key in the sidebar (mockup A).
export const statusOrder: AgentStatus[] = ['busy', 'idle', 'starting', 'stopped', 'attention']

// statusInfo maps a status to its word, color class and icon. The sidebar
// and the panel headers use it, so a status looks the same everywhere.
export function statusInfo(s: AgentStatus): StatusInfo {
  return info[s] ?? { word: String(s), cls: 'c-muted', icon: 'clock' }
}
</script>

<script setup lang="ts">
// A status as a badge: icon, word and color together, never color alone.
import { computed } from 'vue'
import Icon from './Icon.vue'

const props = defineProps<{ status: AgentStatus }>()
const s = computed(() => statusInfo(props.status))
</script>

<template>
  <span class="badge" :class="s.cls"><Icon :name="s.icon" />{{ s.word }}</span>
</template>
