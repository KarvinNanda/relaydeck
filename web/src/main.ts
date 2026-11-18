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

const token = readToken(window.location, sessionStore())
setToken(token)
if (token) store.start()
else store.state.unauthorized = true

createApp(App).mount('#app')
