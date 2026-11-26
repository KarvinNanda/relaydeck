import { describe, expect, it, vi } from 'vitest'
import { useWebgl, type WebglLike } from './termrender'

function fakeAddon() {
  let lost: (() => void) | null = null
  const a = {
    onContextLoss: vi.fn((cb: () => void) => {
      lost = cb
      return { dispose() {} }
    }),
    dispose: vi.fn(),
    lose: () => lost?.(),
  }
  return a
}

describe('useWebgl', () => {
  it('loads the WebGL addon into the terminal', () => {
    const a = fakeAddon()
    const term = { loadAddon: vi.fn() }
    expect(useWebgl(term, () => a as WebglLike)).toBe(true)
    expect(term.loadAddon).toHaveBeenCalledWith(a)
  })

  it('keeps the DOM renderer when WebGL2 is missing (loadAddon throws)', () => {
    const a = fakeAddon()
    const term = {
      loadAddon: vi.fn(() => {
        throw new Error('WebGL2 not supported')
      }),
    }
    expect(useWebgl(term, () => a as WebglLike)).toBe(false)
  })

  it('keeps the DOM renderer when the addon cannot be built', () => {
    const term = { loadAddon: vi.fn() }
    const make = () => {
      throw new Error('Webgl2 is only supported on Safari 16 and above')
    }
    expect(useWebgl(term, make)).toBe(false)
    expect(term.loadAddon).not.toHaveBeenCalled()
  })

  it('disposes the addon when the GPU drops the context, so xterm goes back to the DOM renderer', () => {
    const a = fakeAddon()
    useWebgl({ loadAddon: vi.fn() }, () => a as WebglLike)
    expect(a.dispose).not.toHaveBeenCalled()
    a.lose()
    expect(a.dispose).toHaveBeenCalledTimes(1)
  })
})
