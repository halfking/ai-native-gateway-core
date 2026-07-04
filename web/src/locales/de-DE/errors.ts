// Auto-translated draft (de-DE) · 2026-07-02 · please review
// errors.ts — API error strings. Resolve by the stable backend ApiError.code first
// (errors.code.<code>), falling back to ApiError.message (backend's English/Chinese detail)
// when no code is present. Grow the code list as endpoints evolve.
export default {
  // Resolve by ApiError.code (machine-readable stable strings). Append as new codes appear.
  code: {
    unauthorized: 'Nicht autorisiert, bitte melden Sie sich erneut an',
    forbidden: 'Zugriff verweigert',
    not_found: 'Ressource nicht gefunden',
    rate_limited: 'Zu viele Anfragen, bitte versuchen Sie es bald erneut',
    internal: 'Interner Serverfehler',
  },
  // Fallback by HTTP status.
  byStatus: {
    400: 'Ungültige Anfrage',
    401: 'Nicht autorisiert, bitte melden Sie sich erneut an',
    403: 'Zugriff verweigert',
    404: 'Ressource nicht gefunden',
    409: 'Konflikt',
    429: 'Zu viele Anfragen, bitte versuchen Sie es bald erneut',
    500: 'Interner Serverfehler',
    502: 'Bad Gateway',
    503: 'Dienst nicht verfügbar',
    504: 'Gateway-Zeitüberschreitung',
  },
  network: 'Netzwerkfehler, bitte überprüfen Sie Ihre Verbindung',
  unknown: 'Ein unbekannter Fehler ist aufgetreten',
  /**
   * Bevorzugte Methode zum Anzeigen eines API-Fehlers:
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // gibt die lokalisierte Nachricht für die aktive Sprache zurück
   * Hier sind nur Daten; die Auflösungslogik befindet sich in useApiError.ts.
   */
}