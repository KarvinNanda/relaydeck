import { createApp } from 'vue'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/500.css'
import '@fontsource/jetbrains-mono/600.css'
import '@fontsource/jetbrains-mono/700.css'
import './styles/tokens.css'
import './styles/base.css'
import App from './App.vue'
import { setToken } from './api'
import { readToken } from './token'
import { store } from './stores/state'

const token = readToken(window.location, window.sessionStorage)
setToken(token)
if (token) store.start()
else store.state.unauthorized = true

createApp(App).mount('#app')
