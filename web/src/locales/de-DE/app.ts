// Auto-translated draft (de-DE) · 2026-07-02 · please review
// app.ts — App.vue shell strings (header roles, sidebar collapse, logout, language switcher).
export default {
  brand: {
    siteTitle: 'AI Native Kern-Gateway',
    siteTitleLineTwo: 'Kern-Gateway',
    alt: 'Kaixuan-Logo',
  },
  role: {
    super_admin: 'Super-Administrator',
    tenant_admin: 'Mandanten-Administrator',
  },
  // 2026-07-22: public shell footer strings (paired with LifecycleShell).
  // 2026-07-22: user menu dropdown (UserMenuDropdown) + profile dialog (UserInfoDialog).
  userMenu: {
    profile: 'Profil',
    changePassword: 'Passwort ändern',
    logout: 'Abmelden',
  },
  userInfo: {
    displayName: 'Anzeigename',
    username: 'Benutzername',
    email: 'E-Mail',
    role: 'Rolle',
    tenant: 'Mandant',
    close: 'Schließen',
  },
  // 2026-07-22: skip-link (a11y) strings.

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  dataHorizon: {
    title: 'Traffic-Daten fallen zurück: das Gateway verarbeitet Anfragen, aber session_turns kommt nicht hinterher',
    titleUnavailable: 'Status der Traffic-Daten nicht feststellbar: Die Zahlen auf dieser Seite könnten veraltet sein',
    hintLabel: 'Untersuchen',
    retry: 'Erneut prüfen',
    failedHint: 'Der Datenstatus konnte nicht gelesen werden. Ein unbestätigter Status ist kein gesunder Status. Die Zahlen auf dieser Seite können veraltet sein.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'Ein neuerer Build ist ausgerollt. Laden Sie die Seite neu, um zu wechseln.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'Diese Seite trägt keine Deploy-Sequenz (beim Build nicht eingefügt)',
    reasonNoRemote: 'Der Server hat keine Deploy-Sequenz geliefert (alter Build oder Betrieb außerhalb des Repos)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
