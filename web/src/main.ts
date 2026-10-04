import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import './style.css'
import 'element-plus/dist/index.css'
// EP 组件暗色变量（html.dark 门控）。styles/element-dark.css 对 --el-* 的应用
// 令牌映射依赖本文件中的先后顺序（同特异性后者胜出）。
import 'element-plus/theme-chalk/dark/css-vars.css'
import './styles/element-dark.css'
import './styles/pill-chip.css'
import './styles/confirm-dialog.css'
import './styles/responsive-base.css'
// 2026-10-04 Hyper 移动端壳层（docs/UI规范/00 §5.2）。放在 responsive-base 之后：
// 它的规则更具体（compact 档内分支），需要后加载才能覆盖兜底。
import './styles/hyper.css'
import './styles/foldable.css'
import { initErrorReporter, createVueErrorHandler } from './utils/errorReporter'

// 初始化全局错误上报
initErrorReporter()

const app = createApp(App)

// 配置 Vue 错误处理器
app.config.errorHandler = createVueErrorHandler()

app.use(router).use(i18n).mount('#app')
