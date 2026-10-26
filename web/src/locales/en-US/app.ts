// app.ts — App.vue shell strings (header roles, sidebar collapse, logout, language switcher).
export default {
  brand: {
    siteTitle: 'AI Native Core Gateway',
    siteTitleLineTwo: 'Core Gateway',
    alt: 'Kaixuan logo',
  },
  role: {
    super_admin: 'Super Admin',
    tenant_admin: 'Tenant Admin',
  },
  // 2026-07-21: theme strings aligned with ai-native-maintain / ai-session-manager.
  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  // 2026-07-22: top-bar accessibility strings (incl. a11y skip-link).
  // 2026-07-22: public shell footer strings (used by LifecycleShell).
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
  dataHorizon: {
    title: 'Traffic data is falling behind: the gateway is serving requests but session records are not keeping up',
    titleUnavailable: 'Unable to confirm traffic data state: figures on this page may be stale',
    hintLabel: 'Investigate',
    retry: 'Re-check',
    failedHint: 'Could not read the data state. An unconfirmed state is not a healthy state. Treat figures on this page as possibly stale.',
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
