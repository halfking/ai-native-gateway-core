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
  // 2026-07-22: 顶部水平菜单的无障碍文案 + skip-link。
  nav: {
    mainAria: '主导航',
    skip: '跳到主内容',
  },
  // 2026-07-22: 公开壳页面底部文案（LifecycleShell.footer 三段）。
  footer: {
    left: '© 2026 AI-Native 组织网关',
    right: '需要帮助？',
    feedbackLink: '提交反馈',
  },
  // 2026-07-22: 用户菜单下拉（UserMenuDropdown）+ 用户信息弹窗（UserInfoDialog）。
  userMenu: {
    profile: '个人信息',
    changePassword: '修改密码',
    logout: '退出登录',
  },
  userInfo: {
    displayName: '显示名',
    username: '用户名',
    email: '邮箱',
    role: '角色',
    tenant: '租户',
    close: '关闭',
  },
}
