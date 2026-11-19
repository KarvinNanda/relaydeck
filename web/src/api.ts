// HTTP and WebSocket client for the daemon's UI API. Every /api request
// carries the UI token as a bearer header. A WebSocket cannot carry headers,
// so openSocket first trades the token for a single-use ticket.

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status // 0 means the daemon was not reachable
  }
}

let token: string | null = null

export function setToken(t: string | null): void {
  token = t
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (token) headers.Authorization = `Bearer ${token}`
  const init: RequestInit = { method, headers, cache: 'no-store' }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    throw new ApiError(0, 'relaydeck is not reachable. Is `relaydeck serve` still running?')
  }
  if (!res.ok) throw new ApiError(res.status, await errorText(res))
  const text = await res.text()
  if (text === '') return undefined as T
  try {
    return JSON.parse(text) as T
  } catch {
    throw new ApiError(res.status, 'invalid response from relaydeck')
  }
}

// errorText reads the server's {"error": "..."} body, or the short plain
// text that the auth guard writes. It never returns raw JSON or HTML.
// uploadAttachment sends a dropped or pasted image to the daemon, which saves
// it for the folder's running agent and answers its path. The daemon checks
// the type from the bytes, so the body goes as plain octets.
export async function uploadAttachment(ws: string, folder: string, file: Blob): Promise<string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/octet-stream' }
  if (token) headers.Authorization = `Bearer ${token}`
  const path = `/api/agents/${encodeURIComponent(ws)}/${encodeURIComponent(folder)}/attachments`
  let res: Response
  try {
    res = await fetch(path, { method: 'POST', headers, body: file, cache: 'no-store' })
  } catch {
    throw new ApiError(0, 'relaydeck is not reachable. Is `relaydeck serve` still running?')
  }
  if (!res.ok) throw new ApiError(res.status, await errorText(res))
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = null
  }
  const p = (body as { path?: unknown } | null)?.path
  if (typeof p !== 'string' || p === '') throw new ApiError(res.status, 'invalid response from relaydeck')
  return p
}

async function errorText(res: Response): Promise<string> {
  const fallback = `request failed (HTTP ${res.status})`
  const type = res.headers.get('Content-Type') ?? ''
  let text: string
  try {
    text = await res.text()
  } catch {
    return fallback
  }
  if (type.startsWith('application/json')) {
    try {
      const v: unknown = JSON.parse(text)
      if (v !== null && typeof v === 'object' && typeof (v as { error?: unknown }).error === 'string') {
        return (v as { error: string }).error
      }
    } catch {
      // fall through to the fallback
    }
    return fallback
  }
  if (type.startsWith('text/plain')) {
    const line = text.trim()
    if (line !== '' && line.length <= 200) return line
  }
  return fallback
}

// openSocket gets a WebSocket ticket and opens path with it. It resolves
// once the socket is open and rejects with ApiError when the ticket request
// fails or the socket closes first.
export async function openSocket(path: string): Promise<WebSocket> {
  const { ticket } = await api<{ ticket: string }>('POST', '/api/ws-ticket')
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const sep = path.includes('?') ? '&' : '?'
  const ws = new WebSocket(`${scheme}//${location.host}${path}${sep}ticket=${encodeURIComponent(ticket)}`)
  return new Promise((resolve, reject) => {
    ws.onopen = () => {
      ws.onopen = ws.onerror = ws.onclose = null
      resolve(ws)
    }
    // A failed upgrade fires error and then close; reject once, on close.
    ws.onerror = null
    ws.onclose = () => {
      ws.onopen = ws.onclose = null
      reject(new ApiError(0, `could not open ${path}`))
    }
  })
}
