// src/test-utils/i18n.ts —— 组件挂载测试用的 i18n 实例（单一真值源）。
//
// 为什么需要它
// ------------
// 组件模板里用 `$t('…')` 取文案时，**必须**给挂载出来的 app 装上 i18n 插件：
//   - 不装 ⇒ 渲染期直接抛 `TypeError: _ctx.$t is not a function`；
//   - 更隐蔽的是**普通属性**里的 `$t(…)`（Vue 3 不对普通属性做插值，
//     它会被编译成静态字符串）⇒ tooltip 会把 `$t('common.close')` 原样显示给用户。
//
// 为什么注入**真实 zh-CN 文案**而不是空 messages
// --------------------------------------------
// 空 messages 时 `$t('common.terms.model')` 返回的是**键路径**本身，
// 任何「界面上应当出现中文」的断言都会失配（而结构性的断言照样过 ⇒
// 半绿不绿，最难查）。这里直接挂 `locales/zh-CN` 的聚合导出，
// 断言里的中文与真实界面一致。

import { createI18n } from 'vue-i18n'
import zhCN from '../locales/zh-CN'

export const testI18n = createI18n({
  legacy: false,
  globalInjection: true, // 模板里能直接用 $t
  locale: 'zh-CN',
  fallbackLocale: 'zh-CN',
  messages: { 'zh-CN': zhCN as never },
})

export default testI18n
