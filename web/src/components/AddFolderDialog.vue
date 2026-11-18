<script lang="ts">
// Input rules shared with the server. The server checks again (Task 3 and
// 13); these only give a quick, clear error before the request. Tests read
// the Go source to keep them the same.

// internal/config nameRe: workspace and folder names.
export const nameRule = /^[a-z0-9][a-z0-9-]{0,31}$/
// internal/launch accountRe, plus "must not start with a dash".
export const accountRule = /^[A-Za-z0-9@._+-]+$/

// internal/config permissionModes without "" (empty means auto). Auto is
// first because it is the default.
export const permissionModes = ['auto', 'acceptEdits', 'plan', 'manual', 'dontAsk', 'bypassPermissions'] as const

export function nameError(s: string): string {
  return nameRule.test(s) ? '' : 'Use 1 to 32 lowercase letters, digits or dashes, starting with a letter or digit.'
}

// pathError mirrors the server for its platform: "~", "~/x" or "~\x" on
// every platform, else an absolute path as Go filepath.IsAbs sees it there.
// On darwin and linux that is a POSIX path. On windows it is a drive path
// (C:\x or C:/x) or a UNC path (\\host\share or //host/share).
export function pathError(s: string, platform: string): string {
  if (s === '~' || /^~[/\\]/.test(s)) return ''
  if (platform === 'windows') {
    return /^[A-Za-z]:[/\\]/.test(s) || /^[/\\]{2}[^/\\]/.test(s)
      ? ''
      : 'Enter the full path of an existing folder, starting with a drive like C:\\ or with ~.'
  }
  return s.startsWith('/') ? '' : 'Enter the full path of an existing folder, starting with / or ~.'
}

export function accountError(s: string): string {
  if (s === '') return ''
  return !s.startsWith('-') && accountRule.test(s)
    ? ''
    : 'Use letters, digits and @ . _ + - only, and do not start with a dash.'
}
</script>

<script setup lang="ts">
// Modal form that adds a folder to a workspace: name, path (typed or picked
// with the native folder dialog), account (hidden on windows) and
// permission mode.
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api, ApiError } from '../api'
import { store } from '../stores/state'
import Icon from './Icon.vue'

const props = defineProps<{ ws: string }>()
const emit = defineEmits<{ close: []; added: [name: string] }>()

const form = reactive({ name: '', path: '', account: '', mode: 'auto' as string })
const touched = reactive({ name: false, path: false, account: false })
const submitted = ref(false)
const sending = ref(false)
const picking = ref(false)
const serverError = ref('')

const box = ref<HTMLElement | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)
let returnFocus: HTMLElement | null = null

const windows = computed(() => store.state.data?.platform === 'windows')

const errors = computed(() => ({
  name: nameError(form.name),
  path: pathError(form.path, store.state.data?.platform ?? ''),
  account: windows.value ? '' : accountError(form.account),
}))

function shown(field: 'name' | 'path' | 'account'): string {
  return submitted.value || touched[field] ? errors.value[field] : ''
}

function message(e: unknown): string {
  return e instanceof ApiError ? e.message : 'Something went wrong. Try again.'
}

// A name taken from the picked folder, when it fits the rule.
function suggestName(path: string): string {
  const base = path.split(/[/\\]/).filter(Boolean).pop() ?? ''
  const s = base.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 32)
  return nameError(s) === '' ? s : ''
}

async function choose(): Promise<void> {
  picking.value = true
  try {
    const r = await api<{ path?: string; cancelled?: boolean }>('POST', '/api/pick-folder')
    if (r?.cancelled || !r?.path) return // a cancel keeps any earlier error
    serverError.value = ''
    form.path = r.path
    touched.path = true
    if (form.name === '') form.name = suggestName(r.path)
  } catch (e) {
    serverError.value = message(e)
  } finally {
    picking.value = false
  }
}

async function submit(): Promise<void> {
  submitted.value = true
  serverError.value = ''
  const e = errors.value
  if (e.name || e.path || e.account) {
    await nextTick()
    box.value?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus()
    return
  }
  const body: Record<string, string> = { name: form.name, path: form.path, permission_mode: form.mode }
  if (!windows.value && form.account !== '') body.account = form.account
  sending.value = true
  try {
    await api('POST', `/api/workspaces/${encodeURIComponent(props.ws)}/folders`, body)
    await store.refresh()
    emit('added', form.name)
    emit('close')
  } catch (err) {
    serverError.value = message(err)
  } finally {
    sending.value = false
  }
}

// Keep Tab inside the dialog while it is open.
function trap(ev: KeyboardEvent): void {
  if (ev.key !== 'Tab' || !box.value) return
  const items = [...box.value.querySelectorAll<HTMLElement>('input, select, button')].filter((el) => !el.hasAttribute('disabled'))
  if (items.length === 0) return
  const first = items[0]
  const last = items[items.length - 1]
  if (ev.shiftKey && document.activeElement === first) {
    ev.preventDefault()
    last.focus()
  } else if (!ev.shiftKey && document.activeElement === last) {
    ev.preventDefault()
    first.focus()
  }
}

onMounted(() => {
  returnFocus = document.activeElement as HTMLElement | null
  nameInput.value?.focus()
})
onBeforeUnmount(() => returnFocus?.focus?.())
</script>

<template>
  <div class="scrim">
    <div
      ref="box"
      class="dlg"
      role="dialog"
      aria-modal="true"
      aria-labelledby="af-title"
      @keydown.esc.stop="emit('close')"
      @keydown="trap"
    >
      <div class="dlg-h">
        <h2 id="af-title">Add folder</h2>
        <span class="c-muted">to {{ ws }}</span>
        <button class="ib close" type="button" aria-label="Close" title="Close" @click="emit('close')">
          <Icon name="x" />
        </button>
      </div>

      <form class="dlg-b" novalidate @submit.prevent="submit">
        <div class="field">
          <label for="af-name">Name</label>
          <input
            id="af-name"
            ref="nameInput"
            v-model.trim="form.name"
            type="text"
            autocomplete="off"
            spellcheck="false"
            :aria-invalid="shown('name') ? 'true' : undefined"
            :aria-describedby="shown('name') ? 'af-name-err' : 'af-name-hint'"
            @blur="touched.name = true"
          >
          <p v-if="shown('name')" id="af-name-err" class="err">{{ shown('name') }}</p>
          <p v-else id="af-name-hint" class="hint">Lowercase letters, digits and dashes, up to 32.</p>
        </div>

        <div class="field">
          <label for="af-path">Path</label>
          <div class="path-row">
            <input
              id="af-path"
              v-model.trim="form.path"
              type="text"
              autocomplete="off"
              spellcheck="false"
              placeholder="e.g. ~/code/app"
              :aria-invalid="shown('path') ? 'true' : undefined"
              :aria-describedby="shown('path') ? 'af-path-err' : undefined"
              @blur="touched.path = true"
            >
            <button class="btn pick" type="button" :disabled="picking" @click="choose">
              <Icon name="fplus" />{{ picking ? 'Waiting…' : 'Choose folder' }}
            </button>
          </div>
          <p v-if="shown('path')" id="af-path-err" class="err">{{ shown('path') }}</p>
        </div>

        <div v-if="!windows" class="field">
          <label for="af-account">Account <span class="c-muted">(optional)</span></label>
          <input
            id="af-account"
            v-model.trim="form.account"
            type="text"
            autocomplete="off"
            spellcheck="false"
            :aria-invalid="shown('account') ? 'true' : undefined"
            :aria-describedby="shown('account') ? 'af-account-err' : 'af-account-hint'"
            @blur="touched.account = true"
          >
          <p v-if="shown('account')" id="af-account-err" class="err">{{ shown('account') }}</p>
          <p v-else id="af-account-hint" class="hint">cswap account. Empty uses the cswap mapping of this path, or the default login.</p>
        </div>

        <div class="field">
          <label for="af-mode">Permission mode</label>
          <select id="af-mode" v-model="form.mode">
            <option v-for="m in permissionModes" :key="m" :value="m">{{ m }}</option>
          </select>
        </div>

        <p v-if="serverError" class="srv-err" role="alert">
          <Icon name="attn" /><span>{{ serverError }}</span>
        </p>

        <div class="acts">
          <button class="btn cancel" type="button" @click="emit('close')">Cancel</button>
          <button class="btn btn-primary" type="submit" :disabled="sending">
            <Icon name="plus" />{{ sending ? 'Adding…' : 'Add folder' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.scrim {
  position: fixed; inset: 0; z-index: 20;
  display: grid; place-items: center; padding: var(--s-8);
  background: color-mix(in srgb, var(--bg) 75%, transparent);
}
.dlg {
  width: min(440px, 100%); max-height: calc(100vh - 32px); overflow-y: auto;
  background: var(--surface); border: 1px solid var(--border-strong); border-radius: var(--r);
}
.dlg-h {
  display: flex; align-items: center; gap: var(--s-4);
  padding: var(--s-2) var(--s-2) var(--s-2) var(--s-6); border-bottom: 1px solid var(--border);
}
.dlg-h h2 { font-weight: 700; font-size: var(--fs-lg); }
.dlg-h .close { margin-left: auto; }
.dlg-b { display: grid; gap: var(--s-6); padding: var(--s-6); }
.field { display: grid; gap: var(--s-2); min-width: 0; }
.field label { font-weight: 600; }
.field p { margin: 0; }
.hint { color: var(--text-muted); }
.err { color: var(--red); }
input, select {
  height: var(--control-h); width: 100%; min-width: 0; padding: 0 var(--s-4);
  background: var(--bg); border: 1px solid var(--border); border-radius: var(--r);
}
input::placeholder { color: var(--text-muted); }
input[aria-invalid="true"] { border-color: var(--red); }
.path-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: var(--s-3); }
.btn:disabled { opacity: .6; cursor: default; }
.srv-err {
  display: flex; gap: var(--s-3); align-items: flex-start; margin: 0;
  padding: var(--s-3) var(--s-4); border-radius: var(--r-sm);
  border: 1px solid var(--red-line); background: var(--red-tint); color: var(--red);
  overflow-wrap: anywhere;
}
.srv-err .i { margin-top: 2px; }
.acts { display: flex; justify-content: flex-end; gap: var(--s-4); }

@media (max-width: 767px) {
  .scrim { padding: var(--gutter-mobile); align-items: start; }
  .dlg-b { padding: var(--s-6) var(--s-6) var(--s-8); }
  /* 16px keeps phone browsers from zooming into the field */
  input, select { height: var(--touch-h); font-size: 16px; }
  .path-row { grid-template-columns: minmax(0, 1fr); }
  .acts .btn { flex: 1; justify-content: center; }
}
</style>
