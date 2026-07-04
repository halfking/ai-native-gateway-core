// Auto-translated draft (es-ES) · 2026-07-02 · please review
// errors.ts — Textos de error de API. Resolver por ApiError.code primero, luego por estado HTTP.
export default {
  code: {
    unauthorized: 'No autorizado, vuelva a iniciar sesión',
    forbidden: 'Acceso denegado',
    not_found: 'Recurso no encontrado',
    rate_limited: 'Demasiadas solicitudes, reintente en breve',
    internal: 'Error interno del servidor',
  },
  byStatus: {
    400: 'Solicitud incorrecta',
    401: 'No autorizado, vuelva a iniciar sesión',
    403: 'Acceso denegado',
    404: 'Recurso no encontrado',
    409: 'Conflicto',
    429: 'Demasiadas solicitudes, reintente en breve',
    500: 'Error interno del servidor',
    502: 'Puerta de enlace incorrecta',
    503: 'Servicio no disponible',
    504: 'Tiempo de espera de la puerta de enlace agotado',
  },
  network: 'Error de red, verifique su conexión',
  unknown: 'Se ha producido un error desconocido',
  /**
   * import { resolveApiError } from '@/i18n/useApiError'
   * resolveApiError(err)
   */
}
