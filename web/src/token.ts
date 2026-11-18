// The UI token reaches the browser in the URL fragment of the link that
// `relaydeck serve` prints. A fragment is never sent to the server and never
// lands in a Referer header. It is moved to sessionStorage right away and
// removed from the address bar, so it does not stay in history or bookmarks.

export const TOKEN_KEY = 'relaydeck.token'

// readToken returns the token from the "#token=" fragment (saving it and
// removing the fragment), or else the token saved by an earlier load of this
// tab, or null.
export function readToken(loc: Location, store: Storage): string | null {
  const params = new URLSearchParams(loc.hash.replace(/^#/, ''))
  if (params.has('token')) {
    history.replaceState(history.state, '', loc.pathname + loc.search)
    const t = params.get('token') ?? ''
    if (t !== '') {
      store.setItem(TOKEN_KEY, t)
      return t
    }
  }
  return store.getItem(TOKEN_KEY)
}
