// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// errors.ts — API error strings. Resolve by the stable backend ApiError.code first
// (errors.code.<code>), falling back to ApiError.message (backend's English/Chinese detail)
// when no code is present. Grow the code list as endpoints evolve.
export default {
  // Resolve by ApiError.code (machine-readable stable strings). Append as new codes appear.
  code: {
    unauthorized: 'Non autorisé, veuillez vous reconnecter',
    forbidden: 'Accès refusé',
    not_found: 'Ressource introuvable',
    rate_limited: 'Trop de requêtes, veuillez réessayer bientôt',
    internal: 'Erreur interne du serveur',
  },
  // Fallback by HTTP status.
  byStatus: {
    400: 'Requête invalide',
    401: 'Non autorisé, veuillez vous reconnecter',
    403: 'Accès refusé',
    404: 'Ressource introuvable',
    409: 'Conflit',
    429: 'Trop de requêtes, veuillez réessayer bientôt',
    500: 'Erreur interne du serveur',
    502: 'Passerelle incorrecte',
    503: 'Service indisponible',
    504: 'Délai d\'attente de la passerelle',
  },
  network: 'Erreur réseau, veuillez vérifier votre connexion',
  unknown: 'Une erreur inconnue est survenue',
  /**
   * Façon préférée d'afficher une erreur API :
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // renvoie le message localisé pour la langue active
   * Seules les données vivent ici ; la logique de résolution est dans useApiError.ts.
   */
}