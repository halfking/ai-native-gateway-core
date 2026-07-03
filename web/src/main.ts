import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import './style.css'
import { initErrorReporter, createVueErrorHandler } from './utils/errorReporter'

// 初始化全局错误上报
initErrorReporter()

const app = createApp(App)

// 配置 Vue 错误处理器
app.config.errorHandler = createVueErrorHandler()

app.use(router).use(i18n).mount('#app')
