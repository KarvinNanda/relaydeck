import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readToken, sessionStore, TOKEN_KEY, _resetMemoryToken } from './token'

describe('readToken', () => {
  beforeEach(() => {
    sessionStorage.clear()
    _resetMemoryToken()
    history.replaceState(null, '', '/')
  })

  it('takes the token from the fragment, saves it and removes the fragment', () => {
    history.replaceState(null, '', '/?x=1#token=abc123')
    expect(readToken(window.location, sessionStorage)).toBe('abc123')
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('abc123')
    expect(window.location.hash).toBe('')
    expect(window.location.href).toBe('http://localhost:3000/?x=1')
  })

  it('reads the saved token on reload, when the fragment is gone', () => {
    sessionStorage.setItem(TOKEN_KEY, 'saved-token')
    expect(readToken(window.location, sessionStorage)).toBe('saved-token')
  })

  it('prefers a new token in the fragment over the saved one', () => {
    sessionStorage.setItem(TOKEN_KEY, 'old')
    history.replaceState(null, '', '/#token=new')
    expect(readToken(window.location, sessionStorage)).toBe('new')
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('new')
  })

  it('returns null when there is no token anywhere', () => {
    expect(readToken(window.location, sessionStorage)).toBeNull()
  })

  it('removes an empty token fragment and does not save it', () => {
    history.replaceState(null, '', '/#token=')
    expect(readToken(window.location, sessionStorage)).toBeNull()
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull()
    expect(window.location.hash).toBe('')
  })

  function blocked(): Storage {
    const boom = () => {
      throw new DOMException('blocked', 'SecurityError')
    }
    return { getItem: boom, setItem: boom, removeItem: boom, clear: boom, key: boom, length: 0 } as unknown as Storage
  }

  it('keeps the token in memory when storage blocks the write', () => {
    history.replaceState(null, '', '/#token=abc123')
    expect(readToken(window.location, blocked())).toBe('abc123')
    expect(window.location.hash).toBe('')
    // a second read in the same page (fragment gone, storage still blocked)
    expect(readToken(window.location, blocked())).toBe('abc123')
  })

  it('returns null, not a throw, when storage blocks the read and there is no token', () => {
    expect(readToken(window.location, blocked())).toBeNull()
  })

  it('works when there is no storage object at all', () => {
    history.replaceState(null, '', '/#token=zzz')
    expect(readToken(window.location, null)).toBe('zzz')
  })

  it('sessionStore returns null when reading window.sessionStorage throws', () => {
    const spy = vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    expect(sessionStore()).toBeNull()
    spy.mockRestore()
  })
})
