import { createApp } from 'vue'
// Latin subset only; other scripts fall back to the system monospace font.
import '@fontsource/jetbrains-mono/latin-400.css'
import '@fontsource/jetbrains-mono/latin-500.css'
import '@fontsource/jetbrains-mono/latin-600.css'
import '@fontsource/jetbrains-mono/latin-700.css'
import './styles/tokens.css'
import './styles/base.css'
import App from './App.vue'
import { setToken } from './api'
import { readToken, sessionStore } from './token'
import { store } from './stores/state'
import { loadPrefs, prefs } from './stores/prefs'

// localStorage itself can throw (blocked site data); prefs then stay at their
// defaults for this page load.
function localStore(): Storage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}
loadPrefs(localStore())
// Only user clicks save the drawer state (App.vue, NotifyView.vue), so an
// automatic open for an approval does not stick across reloads.
store.ui.messagesOpen = prefs.messagesOpen

const token = readToken(window.location, sessionStore())
setToken(token)
if (token) store.start()
else store.state.unauthorized = true

createApp(App).mount('#app')
