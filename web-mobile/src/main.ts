import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './styles/theme.css'
import './styles/base.css'

// main.ts — web-mobile 入口。主题由 /m/entry-switch.js 首帧设置（防 FOUC）。
createApp(App).use(router).mount('#app')
