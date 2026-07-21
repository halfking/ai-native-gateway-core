// app.ts — App.vue 外壳框架文案（顶栏角色、侧栏折叠、退出、语言切换、主题）。
export default {
  brand: 'AI-Native 组织网关',
  role: {
    super_admin: '超级管理员',
    tenant_admin: '租户管理员',
  },
  sidebar: {
    expand: '展开侧栏',
    collapse: '收起侧栏',
    collapseMenu: '收起菜单',
  },
  logout: '退出',
  lang: {
    switch: '切换语言',
    label: '语言',
  },
  // 2026-07-21: 与 ai-native-maintain / ai-session-manager 主题文案对齐。
  theme: {
    switchToLight: '切到浅色',
    switchToDark: '切到深色',
    lightTitle: '浅色模式',
    darkTitle: '深色模式',
  },
  // 2026-07-21: 顶部水平菜单的无障碍文案。
  nav: {
    mainAria: '主导航',
  },
  // 2026-07-21: lifecycle shell 页脚文案（开轩启圭 + 反馈链接）。
  footer: {
    left: '开轩启圭 AI Native',
    right: '© 2026 开轩启圭 · ',
    feedbackLink: '反馈',
  },
}
