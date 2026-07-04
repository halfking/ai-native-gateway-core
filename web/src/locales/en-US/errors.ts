// errors.ts — API error strings. Resolve by the stable backend ApiError.code first
// (errors.code.<code>), falling back to ApiError.message (backend's English/Chinese detail)
// when no code is present. Grow the code list as endpoints evolve.
export default {
  // Resolve by ApiError.code (machine-readable stable strings). Append as new codes appear.
  code: {
    unauthorized: 'Unauthorized, please sign in again',
    forbidden: 'Access denied',
    not_found: 'Resource not found',
    rate_limited: 'Too many requests, please retry shortly',
    internal: 'Internal server error',
  },
  // Fallback by HTTP status.
  byStatus: {
    400: 'Bad request',
    401: 'Unauthorized, please sign in again',
    403: 'Access denied',
    404: 'Resource not found',
    409: 'Conflict',
    429: 'Too many requests, please retry shortly',
    500: 'Internal server error',
    502: 'Bad gateway',
    503: 'Service unavailable',
    504: 'Gateway timeout',
  },
  network: 'Network error, please check your connection',
  unknown: 'An unknown error occurred',
  /**
   * Preferred way to render an API error:
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // returns the localized message for the active locale
   * Only data lives here; resolution logic is in useApiError.ts.
   */
}
