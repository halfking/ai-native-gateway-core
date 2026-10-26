// Auto-translated draft (fr-FR) · 2026-07-02 · please review
// errors.ts — API error strings. Resolve by the stable backend ApiError.code first
// (errors.code.<code>), falling back to ApiError.message (backend's English/Chinese detail)
// when no code is present. Grow the code list as endpoints evolve.
export default {
  // Resolve by ApiError.code (machine-readable stable strings). Append as new codes appear.
  // Fallback by HTTP status.
  byStatus: {
    400: 'La validation des paramètres de la requête a échoué. Vérifiez les valeurs saisies, puis réessayez.',
    401: 'L\'accès a été refusé car la session a expiré. Veuillez vous reconnecter.',
    403: 'Cette opération a été refusée. Contactez l\'administrateur pour obtenir les droits nécessaires.',
    404: 'La ressource est introuvable. Vérifiez l\'adresse, puis réessayez.',
    409: 'L\'opération n\'a pas pu aboutir car elle est en conflit avec l\'état actuel. Rechargez la page, puis réessayez.',
    429: 'La requête a été refusée car trop de requêtes ont été envoyées. Réessayez dans un instant.',
    500: 'Le serveur n\'a pas pu effectuer cette opération. Réessayez dans un instant.',
    502: 'La passerelle n\'a pas pu joindre le service serveur. Réessayez dans un instant.',
    503: 'Le service est temporairement indisponible. Réessayez dans un instant.',
    504: 'Le service serveur n\'a pas pu répondre à temps. Réessayez dans un instant.',
  },
  network: 'Le réseau n\'a pas pu joindre le serveur. Vérifiez la connexion, puis réessayez.',
  unknown: 'Une erreur inattendue est survenue et l\'opération n\'a pas pu aboutir. Réessayez dans un instant.',
  /**
   * Façon préférée d'afficher une erreur API :
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // renvoie le message localisé pour la langue active
   * Seules les données vivent ici ; la logique de résolution est dans useApiError.ts.
   */
}