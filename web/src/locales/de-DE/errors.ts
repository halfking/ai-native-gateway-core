// Auto-translated draft (de-DE) · 2026-07-02 · please review
// errors.ts — API error strings. Resolve by the stable backend ApiError.code first
// (errors.code.<code>), falling back to ApiError.message (backend's English/Chinese detail)
// when no code is present. Grow the code list as endpoints evolve.
export default {
  // Resolve by ApiError.code (machine-readable stable strings). Append as new codes appear.
  // Fallback by HTTP status.
  byStatus: {
    400: 'Die Anfrageparameter sind ungültig. Bitte die Eingaben prüfen und erneut versuchen.',
    401: 'Die Anmeldedaten sind ungültig. Bitte erneut anmelden.',
    403: 'Der Zugriff wurde verweigert. Bitte den Administrator kontaktieren.',
    404: 'Die Ressource wurde nicht gefunden. Bitte die Adresse prüfen.',
    409: 'Der Vorgang konnte nicht abgeschlossen werden, weil er im aktuellen Zustand kollidiert. Bitte die Seite neu laden und erneut versuchen.',
    429: 'Die Anfrage konnte nicht angenommen werden, weil zu viele Versuche liefen. Bitte später erneut versuchen.',
    500: 'Die Aktion ist auf dem Server fehlgeschlagen. Bitte in Kürze erneut versuchen.',
    502: 'Die Verbindung zum Backend-Dienst ist fehlgeschlagen. Bitte in Kürze erneut versuchen.',
    503: 'Der Dienst ist vorübergehend nicht verfügbar. Bitte in Kürze erneut versuchen.',
    504: 'Der Backend-Dienst konnte nicht rechtzeitig antworten. Bitte in Kürze erneut versuchen.',
  },
  network: 'Die Verbindung zum Server ist fehlgeschlagen. Bitte die Verbindung prüfen und erneut versuchen.',
  unknown: 'Es ist ein unerwarteter Fehler aufgetreten. Bitte den Vorgang erneut versuchen.',
  /**
   * Bevorzugte Methode zum Anzeigen eines API-Fehlers:
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // gibt die lokalisierte Nachricht für die aktive Sprache zurück
   * Hier sind nur Daten; die Auflösungslogik befindet sich in useApiError.ts.
   */
}