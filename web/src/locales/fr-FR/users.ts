// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// users.ts — Page de gestion des utilisateurs. Réutilise le module common pour les termes partagés.
export default {
  title: 'Gestion des utilisateurs',
  create: 'Nouvel utilisateur',
  readOnlyNotice: '📖 Vous êtes un administrateur de locataire en mode lecture seule. La gestion des utilisateurs est en lecture seule ; vous ne pouvez pas créer, modifier ou supprimer d\'utilisateurs.',
  filter: {
    byTenant: 'Filtrer par locataire :',
    allTenants: 'Tous les locataires',
  },
  table: {
    username: 'Nom d\'utilisateur',
    displayName: 'Nom affiché',
    email: 'E-mail',
    tenant: 'Locataire',
    role: 'Rôle',
    lastLogin: 'Dernière connexion',
  },
  action: {
    resetPassword: 'Réinitialiser le mot de passe',
  },
  role: {
    super_admin: 'Super administrateur',
    tenant_admin: 'Administrateur de locataire',
  },
  modal: {
    create: {
      title: 'Nouvel utilisateur',
      username: 'Nom d\'utilisateur *',
      password: 'Mot de passe *',
      passwordPlaceholder: 'Au moins 8 caractères',
      displayName: 'Nom affiché',
      email: 'E-mail',
      tenant: 'Locataire *',
      role: 'Rôle',
    },
    reset: {
      title: 'Réinitialiser le mot de passe — {name}',
      newPassword: 'Nouveau mot de passe',
      passwordPlaceholder: 'Au moins 8 caractères',
    },
  },
  error: {
    usernamePasswordRequired: 'Le nom d\'utilisateur et le mot de passe sont requis',
    createFailed: 'Échec de la création',
    resetFailed: 'Échec de la réinitialisation',
    passwordMinLength: 'Le mot de passe doit comporter au moins 8 caractères',
  },
  confirmDelete: 'Supprimer l\'utilisateur {name} ?',
}
