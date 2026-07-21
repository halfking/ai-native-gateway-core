// app.ts — App.vue shell strings (header roles, sidebar collapse, logout, language switcher).
export default {
  brand: 'AI-Native Org Gateway',
  role: {
    super_admin: 'Super Admin',
    tenant_admin: 'Tenant Admin',
  },
  sidebar: {
    expand: 'Expand sidebar',
    collapse: 'Collapse sidebar',
    collapseMenu: 'Collapse menu',
  },
  logout: 'Sign out',
  lang: {
    switch: 'Switch language',
    label: 'Language',
  },
  // 2026-07-21: theme strings aligned with ai-native-maintain / ai-session-manager.
  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  // 2026-07-21: top-bar accessibility strings.
  nav: {
    mainAria: 'Main navigation',
  },
  // 2026-07-21: lifecycle shell footer strings.
  footer: {
    left: 'Qigui AI Native',
    right: '© 2026 Qigui · ',
    feedbackLink: 'Feedback',
  },
}
