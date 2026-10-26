// Auto-translated draft (es-ES) · 2026-07-02 · please review
// errors.ts — Textos de error de API. Resolver por ApiError.code primero, luego por estado HTTP.
export default {
  byStatus: {
    400: 'No se pudo validar los parámetros de la solicitud. Compruebe los parámetros y vuelva a intentarlo.',
    401: 'No se pudo validar la sesión. Por favor, vuelva a iniciar sesión.',
    403: 'El acceso fue denegado. Contacte con el administrador para conceder los permisos.',
    404: 'Recurso no encontrado. Compruebe que la dirección sea correcta.',
    409: 'La operación no se pudo completar por un conflicto con el estado actual. Recargue la página y vuelva a intentarlo.',
    429: 'Demasiadas solicitudes: no se pudo atender la petición. Vuelva a intentarlo en breve.',
    500: 'La operación no se pudo completar en el servidor. Vuelva a intentarlo en breve.',
    502: 'La puerta de enlace no se pudo conectar con el servicio de destino. Vuelva a intentarlo en breve.',
    503: 'Servicio no disponible de forma temporal. Vuelva a intentarlo en breve.',
    504: 'No se pudo obtener una respuesta del servicio de destino a tiempo. Vuelva a intentarlo en breve.',
  },
  network: 'No se pudo conectar con el servidor por red. Compruebe la conexión y vuelva a intentarlo.',
  unknown: 'Ocurrió un error inesperado y la operación no se pudo completar. Vuelva a intentarlo.',
  /**
   * import { resolveApiError } from '@/i18n/useApiError'
   * resolveApiError(err)
   */
}
