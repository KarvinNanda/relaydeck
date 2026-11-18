import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import StatusBadge, { statusInfo } from './StatusBadge.vue'
import type { AgentStatus } from '../types'

const cases: [AgentStatus, string, string, string][] = [
  ['busy', 'busy', 'c-busy', 'busy'],
  ['idle', 'idle', 'c-idle', 'idle'],
  ['attention', 'needs attention', 'c-attn', 'attn'],
  ['stopped', 'stopped', 'c-stop', 'stop'],
  ['starting', 'starting', 'c-start', 'start'],
]

describe('StatusBadge', () => {
  it.each(cases)('%s shows the word "%s", class %s and an icon', (status, word, cls, icon) => {
    const w = mount(StatusBadge, { props: { status } })
    const el = w.find('.badge')
    expect(el.text()).toBe(word)
    expect(el.classes()).toContain(cls)
    // the color never stands alone: an icon and the word go with it
    expect(el.find('svg.i').exists()).toBe(true)
    expect(statusInfo(status)).toEqual({ word, cls, icon })
  })

  it('falls back to a muted badge for a status it does not know', () => {
    const w = mount(StatusBadge, { props: { status: 'weird' as AgentStatus } })
    expect(w.find('.badge').text()).toBe('weird')
    expect(w.find('.badge').classes()).toContain('c-muted')
  })
})
