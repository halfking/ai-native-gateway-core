// Auto-translated draft (de-DE) · 2026-07-02 · please review
// users.ts — Benutzerverwaltungsseite. Verwendet das Common-Modul für gemeinsame Begriffe.
export default {
  title: 'Benutzerverwaltung',
  create: 'Neuen Benutzer anlegen',
  readOnlyNotice: '📖 Sie sind ein Mandantenadministrator im schreibgeschützten Modus. Die Benutzerverwaltung ist nur lesbar; Sie können keine Benutzer erstellen, bearbeiten oder löschen.',
  filter: {
    byTenant: 'Nach Mandant filtern:',
    allTenants: 'Alle Mandanten',
  },
  table: {
    username: 'Benutzername',
    displayName: 'Anzeigename',
    email: 'E-Mail',
    tenant: 'Mandant',
    role: 'Rolle',
    lastLogin: 'Letzte Anmeldung',
  },
  action: {
    resetPassword: 'Passwort zurücksetzen',
  },
  role: {
    super_admin: 'Super-Administrator',
    tenant_admin: 'Mandantenadministrator',
  },
  modal: {
    create: {
      title: 'Neuen Benutzer anlegen',
      username: 'Benutzername *',
      password: 'Passwort *',
      passwordPlaceholder: 'Mindestens 8 Zeichen',
      displayName: 'Anzeigename',
      email: 'E-Mail',
      tenant: 'Mandant *',
      role: 'Rolle',
    },
    reset: {
      title: 'Passwort zurücksetzen — {name}',
      newPassword: 'Neues Passwort',
      passwordPlaceholder: 'Mindestens 8 Zeichen',
    },
  },
  error: {
    usernamePasswordRequired: 'Benutzername und Passwort sind erforderlich',
    createFailed: 'Erstellen fehlgeschlagen',
    resetFailed: 'Zurücksetzen fehlgeschlagen',
    passwordMinLength: 'Das Passwort muss mindestens 8 Zeichen lang sein',
  },
  confirmDelete: 'Benutzer {name} löschen?',
}
