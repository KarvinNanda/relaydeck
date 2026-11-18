// The UI token reaches the browser in the URL fragment of the link that
// `relaydeck serve` prints. A fragment is never sent to the server and never
// lands in a Referer header. It is moved to sessionStorage right away and
// removed from the address bar, so it does not stay in history or bookmarks.
//
// Some browsers block storage (site data blocked, some private modes): then
// even reading `window.sessionStorage` can throw. The token is kept in memory
// for this page, so the app still works. A reload then needs the link again.

export const TOKEN_KEY = 'relaydeck.token'

let memoryToken: string | null = null

// Test helper: forget the in-memory token.
export function _resetMemoryToken(): void {
  memoryToken = null
}

// sessionStore returns window.sessionStorage, or null when the browser
// blocks it (the property getter itself can throw).
export function sessionStore(): Storage | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

// readToken returns the token from the "#token=" fragment (saving it and
// removing the fragment), or else the token saved by an earlier load of this
// tab, or the one kept in memory, or null.
export function readToken(loc: Location, store: Storage | null): string | null {
  const params = new URLSearchParams(loc.hash.replace(/^#/, ''))
  if (params.has('token')) {
    history.replaceState(history.state, '', loc.pathname + loc.search)
    const t = params.get('token') ?? ''
    if (t !== '') {
      memoryToken = t
      try {
        store?.setItem(TOKEN_KEY, t)
      } catch {
        // blocked: the in-memory copy is enough for this page
      }
      return t
    }
  }
  try {
    return store?.getItem(TOKEN_KEY) ?? memoryToken
  } catch {
    return memoryToken
  }
}
