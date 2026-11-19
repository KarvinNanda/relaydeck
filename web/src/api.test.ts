import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, openSocket, setToken, uploadAttachment } from './api'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('api', () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    setToken('tok-1')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('sends the bearer token and returns the parsed JSON', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { platform: 'darwin' }))
    const got = await api<{ platform: string }>('GET', '/api/state')
    expect(got).toEqual({ platform: 'darwin' })
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/state')
    expect(init?.method).toBe('GET')
    expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer tok-1')
    expect(init?.body).toBeUndefined()
  })

  it('sends a body as JSON', async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { name: 'w' }))
    await api('POST', '/api/workspaces', { name: 'w' })
    const init = fetchMock.mock.calls[0][1]
    expect(new Headers(init?.headers).get('Content-Type')).toBe('application/json')
    expect(init?.body).toBe('{"name":"w"}')
  })

  it('returns undefined for 204 No Content', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(api('POST', '/api/agents/w/a/stop')).resolves.toBeUndefined()
  })

  it('throws ApiError with status 401 on a plain text 401', async () => {
    fetchMock.mockResolvedValue(new Response('unauthorized\n', { status: 401, headers: { 'Content-Type': 'text/plain; charset=utf-8' } }))
    const err = await api('GET', '/api/state').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect((err as ApiError).message).toBe('unauthorized')
  })

  it('puts the server error text from {"error": ...} into the message', async () => {
    fetchMock.mockResolvedValue(jsonResponse(409, { error: 'agent has a draft; stop with force' }))
    const err = (await api('POST', '/api/agents/w/a/stop').catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(409)
    expect(err.message).toBe('agent has a draft; stop with force')
  })

  it('never puts raw JSON into the message', async () => {
    fetchMock.mockResolvedValue(jsonResponse(500, { detail: 'x' }))
    const err = (await api('GET', '/api/state').catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(500)
    expect(err.message).toBe('request failed (HTTP 500)')
  })

  it('throws ApiError when a 2xx body is not JSON', async () => {
    fetchMock.mockResolvedValue(new Response('<!doctype html><p>hi</p>', { status: 200, headers: { 'Content-Type': 'text/html' } }))
    const err = (await api('GET', '/api/state').catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(200)
    expect(err.message).toBe('invalid response from relaydeck')
  })

  it('throws ApiError with status 0 when the daemon is not reachable', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    const err = (await api('GET', '/api/state').catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(0)
  })
})

class FakeWebSocket {
  static last: FakeWebSocket | null = null
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  constructor(public url: string) {
    FakeWebSocket.last = this
  }
  close() {}
}

describe('openSocket', () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('WebSocket', FakeWebSocket)
    setToken('tok-1')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('gets a ticket first and opens the socket with it', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { ticket: 't/1+2' }))
    const p = openSocket('/ws/events')
    await vi.waitFor(() => expect(FakeWebSocket.last).not.toBeNull())
    const ws = FakeWebSocket.last!
    expect(fetchMock.mock.calls[0][0]).toBe('/api/ws-ticket')
    expect(fetchMock.mock.calls[0][1]?.method).toBe('POST')
    expect(ws.url).toBe('ws://localhost:3000/ws/events?ticket=t%2F1%2B2')
    ws.onopen?.(new Event('open'))
    await expect(p).resolves.toBe(ws)
  })

  it('rejects when the socket closes before it opens', async () => {
    FakeWebSocket.last = null
    fetchMock.mockResolvedValue(jsonResponse(200, { ticket: 't' }))
    const p = openSocket('/ws/events')
    await vi.waitFor(() => expect(FakeWebSocket.last).not.toBeNull())
    FakeWebSocket.last!.onclose?.(new CloseEvent('close'))
    await expect(p).rejects.toBeInstanceOf(ApiError)
  })

  it('passes a 401 from the ticket request through', async () => {
    fetchMock.mockResolvedValue(new Response('unauthorized\n', { status: 401, headers: { 'Content-Type': 'text/plain' } }))
    const err = (await openSocket('/ws/events').catch((e: unknown) => e)) as ApiError
    expect(err.status).toBe(401)
  })
})

describe('uploadAttachment', () => {
  const fetchMock = vi.fn<typeof fetch>()
  beforeEach(() => {
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    setToken('tok-1')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('posts the raw file with the bearer token and returns the path', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { path: '/rt/a/attachments/x.png' }))
    const file = new Blob(['png-bytes'], { type: 'image/png' })
    await expect(uploadAttachment('w s', 'f', file)).resolves.toBe('/rt/a/attachments/x.png')
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/agents/w%20s/f/attachments')
    expect(init?.method).toBe('POST')
    const h = new Headers(init?.headers)
    expect(h.get('Authorization')).toBe('Bearer tok-1')
    expect(h.get('Content-Type')).toBe('application/octet-stream')
    expect(init?.body).toBe(file)
  })

  it('turns an error answer into an ApiError with its text', async () => {
    fetchMock.mockResolvedValue(jsonResponse(415, { error: 'only PNG, JPEG, GIF and WebP images can be attached' }))
    const err = (await uploadAttachment('w', 'f', new Blob(['x'])).catch((e: unknown) => e)) as ApiError
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(415)
    expect(err.message).toBe('only PNG, JPEG, GIF and WebP images can be attached')
  })
})
