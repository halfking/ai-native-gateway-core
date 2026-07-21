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
  // 2026-07-22: top-bar accessibility strings (incl. a11y skip-link).
  nav: {
    mainAria: 'Main navigation',
    skip: 'Skip to main content',
  },
  // 2026-07-22: public shell footer strings (used by LifecycleShell).
  footer: {
    left: '© 2026 AI-Native Org Gateway',
    right: 'Need help?',
    feedbackLink: 'Send feedback',
  },
  // 2026-07-22: user menu dropdown (UserMenuDropdown) + profile dialog (UserInfoDialog).
  userMenu: {
    profile: 'Profile',
    changePassword: 'Change password',
    logout: 'Sign out',
  },
  userInfo: {
    displayName: 'Display name',
    username: 'Username',
    email: 'Email',
    role: 'Role',
    tenant: 'Tenant',
    close: 'Close',
  },
}
