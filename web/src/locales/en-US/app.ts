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
  v1DataFrozen: {
    title: 'Traffic data has stopped updating: figures on this page reflect only the period before stop-write',
    titleUnconfirmed: 'Stop-write switch is not explicitly configured: cannot tell whether these figures are stale',
    titleUnavailable: 'Unable to confirm traffic data state: figures on this page may be stale',
    affects: 'Affected read-point classes',
    gateKey: 'Controlling setting',
    retry: 'Re-check',
    failedHint: 'Could not read the data state. An unconfirmed state is not a healthy state — treat figures on this page as possibly stale.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'A newer build is deployed. Reload the page to switch.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'This page carries no deploy seq (not injected at build time)',
    reasonNoRemote: 'Server returned no deploy seq (old build or non-repo hosting)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
