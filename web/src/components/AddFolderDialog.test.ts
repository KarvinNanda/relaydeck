import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '../api'
import { store } from '../stores/state'
import AddFolderDialog, { accountError, nameError, nameRule, pathError, permissionModes } from './AddFolderDialog.vue'
import type { State } from '../types'

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>()
  return { ...real, api: vi.fn() }
})
const apiMock = vi.mocked(api)

// Go source of the server, read from the repo (web/src/components -> internal).
function goSource(rel: string): string {
  const here = dirname(expect.getState().testPath ?? '')
  return readFileSync(join(here, '../../../internal', rel), 'utf8')
}

function snapshot(platform = 'darwin'): State {
  return {
    platform,
    account: { cswap: true, known: true, default: '1' },
    workspaces: [{ name: 'game-lounge', folders: [] }],
  }
}

function mountDialog() {
  return mount(AddFolderDialog, { props: { ws: 'game-lounge' }, attachTo: document.body })
}

beforeEach(() => {
  store.state.data = snapshot()
  apiMock.mockReset()
  apiMock.mockImplementation(async (method: string, path: string) => {
    if (method === 'GET' && path === '/api/state') return snapshot()
    throw new Error(`unexpected ${method} ${path}`)
  })
})

afterEach(() => {
  store.state.data = null
  document.body.innerHTML = ''
})

describe('shared rules', () => {
  it('the name rule is the one in internal/config', () => {
    const m = goSource('config/config.go').match(/nameRe = regexp\.MustCompile\(`([^`]+)`\)/)
    expect(m?.[1]).toBe(nameRule.source)
    expect(nameError('be')).toBe('')
    expect(nameError('fe-admin-2')).toBe('')
    expect(nameError('a'.repeat(32))).toBe('')
    expect(nameError('a'.repeat(33))).not.toBe('')
    expect(nameError('')).not.toBe('')
    expect(nameError('-x')).not.toBe('')
    expect(nameError('Be')).not.toBe('')
    expect(nameError('my app')).not.toBe('')
  })

  it('the permission modes are the ones in internal/config, auto first', () => {
    const block = goSource('config/config.go').match(/permissionModes = map\[string\]bool\{([^}]+)\}/)?.[1] ?? ''
    const goModes = [...block.matchAll(/"([^"]*)": true/g)].map((m) => m[1]).filter((s) => s !== '')
    expect([...permissionModes].sort()).toEqual(goModes.sort())
    expect(permissionModes[0]).toBe('auto')
  })

  it('the account rule is the one in internal/launch', () => {
    const m = goSource('launch/launch.go').match(/accountRe = regexp\.MustCompile\(`([^`]+)`\)/)
    expect(m?.[1]).toBe('^[A-Za-z0-9@._+-]+$')
    expect(accountError('')).toBe('')
    expect(accountError('work-2')).toBe('')
    expect(accountError('me+x@example.com')).toBe('')
    expect(accountError('-x')).not.toBe('')
    expect(accountError('a b')).not.toBe('')
    expect(accountError('a;b')).not.toBe('')
  })

  it('a path must be absolute or start with ~, as the server requires', () => {
    expect(pathError('/Users/x/app')).toBe('')
    expect(pathError('~')).toBe('')
    expect(pathError('~/code/app')).toBe('')
    expect(pathError('C:\\code\\app')).toBe('')
    expect(pathError('\\\\server\\share')).toBe('')
    expect(pathError('')).not.toBe('')
    expect(pathError('code/app')).not.toBe('')
    expect(pathError('~other/app')).not.toBe('')
  })
})

describe('AddFolderDialog', () => {
  it('shows name, path, account and permission mode, with auto selected', () => {
    const w = mountDialog()
    expect(w.find('[role="dialog"]').attributes('aria-modal')).toBe('true')
    expect(w.find('h2').text()).toBe('Add folder')
    expect(w.find('input#af-name').exists()).toBe(true)
    expect(w.find('input#af-path').exists()).toBe(true)
    expect(w.find('input#af-account').exists()).toBe(true)
    const sel = w.find('select#af-mode').element as HTMLSelectElement
    expect(sel.value).toBe('auto')
    expect([...sel.options].map((o) => o.value)).toEqual([...permissionModes])
    expect(w.text()).toContain('game-lounge')
    expect(document.activeElement).toBe(w.find('input#af-name').element)
    w.unmount()
  })

  it('hides the account field on windows', () => {
    store.state.data = snapshot('windows')
    const w = mountDialog()
    expect(w.find('input#af-account').exists()).toBe(false)
    expect(w.text()).not.toContain('Account')
    w.unmount()
  })

  it('checks the fields in the client and sends nothing when one is wrong', async () => {
    const w = mountDialog()
    await w.find('input#af-name').setValue('Bad Name')
    await w.find('input#af-path').setValue('relative/path')
    await w.find('input#af-account').setValue('-x')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(apiMock).not.toHaveBeenCalled()
    const name = w.find('input#af-name')
    expect(name.attributes('aria-invalid')).toBe('true')
    const err = w.find(`#${name.attributes('aria-describedby')}`)
    expect(err.text()).toContain('lowercase')
    expect(w.find('input#af-path').attributes('aria-invalid')).toBe('true')
    expect(w.find('input#af-account').attributes('aria-invalid')).toBe('true')
    w.unmount()
  })

  it('posts the folder, refreshes the state and emits added and close', async () => {
    apiMock.mockImplementation(async (method: string, path: string) => {
      if (method === 'POST' && path === '/api/workspaces/game-lounge/folders') return { name: 'be' }
      if (method === 'GET' && path === '/api/state') return snapshot()
      throw new Error(`unexpected ${method} ${path}`)
    })
    const w = mountDialog()
    await w.find('input#af-name').setValue('be')
    await w.find('input#af-path').setValue('~/code/be')
    await w.find('input#af-account').setValue('work-2')
    await w.find('select#af-mode').setValue('plan')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/workspaces/game-lounge/folders', {
      name: 'be',
      path: '~/code/be',
      account: 'work-2',
      permission_mode: 'plan',
    })
    expect(apiMock).toHaveBeenCalledWith('GET', '/api/state')
    expect(w.emitted('added')?.[0]).toEqual(['be'])
    expect(w.emitted('close')).toBeTruthy()
    w.unmount()
  })

  it('leaves out an empty account', async () => {
    apiMock.mockImplementation(async (method: string) => (method === 'GET' ? snapshot() : { name: 'be' }))
    const w = mountDialog()
    await w.find('input#af-name').setValue('be')
    await w.find('input#af-path').setValue('/srv/be')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(apiMock.mock.calls[0][2]).toEqual({ name: 'be', path: '/srv/be', permission_mode: 'auto' })
    w.unmount()
  })

  it('shows the server error text inline and stays open', async () => {
    apiMock.mockImplementation(async () => {
      throw new ApiError(409, 'folder "be" already exists in workspace "game-lounge"')
    })
    const w = mountDialog()
    await w.find('input#af-name').setValue('be')
    await w.find('input#af-path').setValue('/srv/be')
    await w.find('form').trigger('submit')
    await flushPromises()
    const alert = w.find('[role="alert"]')
    expect(alert.text()).toContain('folder "be" already exists in workspace "game-lounge"')
    expect(w.emitted('close')).toBeFalsy()
    expect(w.emitted('added')).toBeFalsy()
    w.unmount()
  })

  it('Choose folder fills the path and a name from the folder', async () => {
    apiMock.mockImplementation(async (method: string, path: string) => {
      if (path === '/api/pick-folder') return { path: '/Users/x/code/fe-admin' }
      throw new Error(`unexpected ${method} ${path}`)
    })
    const w = mountDialog()
    await w.find('button.pick').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/pick-folder')
    expect((w.find('input#af-path').element as HTMLInputElement).value).toBe('/Users/x/code/fe-admin')
    expect((w.find('input#af-name').element as HTMLInputElement).value).toBe('fe-admin')
    w.unmount()
  })

  it('Choose folder keeps a name the user typed', async () => {
    apiMock.mockImplementation(async () => ({ path: '/Users/x/code/fe-admin' }))
    const w = mountDialog()
    await w.find('input#af-name').setValue('admin')
    await w.find('button.pick').trigger('click')
    await flushPromises()
    expect((w.find('input#af-name').element as HTMLInputElement).value).toBe('admin')
    w.unmount()
  })

  it('a cancelled Choose folder changes nothing', async () => {
    apiMock.mockImplementation(async () => ({ cancelled: true }))
    const w = mountDialog()
    await w.find('input#af-path').setValue('/typed/path')
    await w.find('button.pick').trigger('click')
    await flushPromises()
    expect((w.find('input#af-path').element as HTMLInputElement).value).toBe('/typed/path')
    expect((w.find('input#af-name').element as HTMLInputElement).value).toBe('')
    expect(w.find('[role="alert"]').exists()).toBe(false)
    w.unmount()
  })

  it('shows a Choose folder error inline', async () => {
    apiMock.mockImplementation(async () => {
      throw new ApiError(500, 'could not open the folder dialog; type the path instead')
    })
    const w = mountDialog()
    await w.find('button.pick').trigger('click')
    await flushPromises()
    expect(w.find('[role="alert"]').text()).toContain('could not open the folder dialog; type the path instead')
    w.unmount()
  })

  it('Escape and Cancel emit close', async () => {
    const w = mountDialog()
    await w.find('[role="dialog"]').trigger('keydown', { key: 'Escape' })
    expect(w.emitted('close')).toHaveLength(1)
    await w.find('button.cancel').trigger('click')
    expect(w.emitted('close')).toHaveLength(2)
    w.unmount()
  })
})
