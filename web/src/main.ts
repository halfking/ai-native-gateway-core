import { createApp, nextTick } from 'vue'
import App from './App.vue'
import { router } from './router'
import { enableAuthRedirect } from './api/_core'
import './style.css'

const app = createApp(App)
app.use(router)
app.mount('#app')

// Re-enable the api/_core 401 → /login redirect AFTER the very first
// paint has settled. The redirect is suppressed during the initial
// mount window so that a stale token does not produce a one-frame
// flash of the protected page followed by an immediate jump to /login
// (reported as "页面闪了一下就消失了", 2026-06-26).
// Two animation frames give the layout a chance to commit before any
// background fetch failure can yank the user away.
requestAnimationFrame(() => {
  requestAnimationFrame(() => {
    nextTick(() => {
      enableAuthRedirect()
    })
  })
})
