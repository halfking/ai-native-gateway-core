// Auto-translated draft (de-DE) · 2026-07-02 · please review
// app.ts — App.vue shell strings (header roles, sidebar collapse, logout, language switcher).
export default {
  brand: 'AI-Native Org Gateway',
  role: {
    super_admin: 'Super-Administrator',
    tenant_admin: 'Mandanten-Administrator',
  },
  sidebar: {
    expand: 'Seitenleiste ausklappen',
    collapse: 'Seitenleiste einklappen',
    collapseMenu: 'Menü einklappen',
  },
  logout: 'Abmelden',
  lang: {
    switch: 'Sprache wechseln',
    label: 'Sprache',
  },
  // 2026-07-22: public shell footer strings (paired with LifecycleShell).
  footer: {
    left: '© 2026 AI-Native Gateway',
    right: 'Brauchen Sie Hilfe?',
    feedbackLink: 'Feedback senden',
  },
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
  nav: {
    mainAria: 'Hauptnavigation',
    skip: 'Zum Hauptinhalt springen',
  },

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  v1DataFrozen: {
    title: 'Traffic-Daten werden nicht mehr aktualisiert: Die Zahlen auf dieser Seite entsprechen nur dem Zeitraum vor dem Schreibstopp',
    titleUnconfirmed: 'Abschreibeschalter ist nicht explizit konfiguriert: Ob diese Zahlen veraltet sind, lässt sich nicht feststellen',
    titleUnavailable: 'Status der Traffic-Daten nicht feststellbar: Die Zahlen auf dieser Seite könnten veraltet sein',
    affects: 'Betroffene Lesepunkt-Klassen',
    gateKey: 'Steuernde Einstellung',
    retry: 'Erneut prüfen',
    failedHint: 'Der Datenstatus konnte nicht gelesen werden. Ein unbestätigter Status ist kein gesunder Status — die Zahlen auf dieser Seite können veraltet sein.',
  },
}