import { beforeEach, describe, expect, it } from 'vitest'
import { readToken, TOKEN_KEY } from './token'

describe('readToken', () => {
  beforeEach(() => {
    sessionStorage.clear()
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
})
