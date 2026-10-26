// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// app.ts — App.vue shell strings (header roles, sidebar collapse, logout, language switcher).
export default {
  brand: {
    siteTitle: 'AI Native Passerelle Centrale',
    siteTitleLineTwo: 'Passerelle Centrale',
    alt: 'Logo Kaixuan',
  },
  role: {
    super_admin: 'Super-administrateur',
    tenant_admin: 'Administrateur du locataire',
  },
  // 2026-07-22: public shell footer strings (paired with LifecycleShell).
  // 2026-07-22: user menu dropdown (UserMenuDropdown) + profile dialog (UserInfoDialog).
  userMenu: {
    profile: 'Profil',
    changePassword: 'Modifier le mot de passe',
    logout: 'Se déconnecter',
  },
  userInfo: {
    displayName: 'Nom affiché',
    username: "Nom d'utilisateur",
    email: 'E-mail',
    role: 'Rôle',
    tenant: 'Locataire',
    close: 'Fermer',
  },
  // 2026-07-22: skip-link (a11y) strings.

  theme: {
    switchToLight: 'Switch to light',
    switchToDark: 'Switch to dark',
    lightTitle: 'Light mode',
    darkTitle: 'Dark mode',
  },
  dataHorizon: {
    title: 'Les données de trafic prennent du retard : la passerelle traite des requêtes mais le flux des sessions ne suit pas',
    titleUnavailable: 'Impossible de confirmer l’état des données de trafic : les chiffres de cette page peuvent être obsolètes',
    hintLabel: 'Inspecter',
    retry: 'Revérifier',
    failedHint: 'Lecture de l’état des données impossible. Un état non confirmé n’est pas un état sain. Traitez les chiffres de cette page comme potentiellement obsolètes.',
  },
  deploySeq: {
    bannerTitle: 'New version available',
    bannerHint: 'Une version plus récente est déployée. Rechargez la page pour passer à la nouvelle.',
    updateNow: 'Reload now',
    later: 'Later',
    currentSeq: 'Page deploy seq',
    state: 'Update state',
    stateLatest: 'Up to date',
    stateAvailable: 'Update available',
    stateIndeterminate: 'Indeterminate',
    stateChecking: 'Checking…',
    reasonNoLocal: 'Cette page ne porte pas de séquence de déploiement (non injectée à la compilation)',
    reasonNoRemote: 'Le serveur n\'a pas renvoyé de séquence de déploiement (version ancienne ou hébergement hors dépôt)',
    reasonNetwork: 'Check request failed; cannot determine',
    checks: 'Checks run',
    checkNow: 'Check for updates',
    sourceUnknown: 'Not injected',
  },
}
