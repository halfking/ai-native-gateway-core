import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { wireApp } from './hyper-wiring'
import { useAuthStore } from './stores/auth'
import './styles/theme.css'
import './styles/shared.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)

wireApp(router)

app.use(router)
app.mount('#app')

// 冷启动水合探测（/api/auth/me）：cookie 有效 → 恢复 userInfo（web/store.ts
// 同源语义）；路由守卫会等待 authHydrated。
const auth = useAuthStore()
void auth.hydrate()
