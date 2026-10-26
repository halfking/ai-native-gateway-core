// errors.ts — API error strings. Resolve by the stable backend ApiError.code first
// (errors.code.<code>), falling back to ApiError.message (backend's English/Chinese detail)
// when no code is present. Grow the code list as endpoints evolve.
export default {
  // Resolve by ApiError.code (machine-readable stable strings). Append as new codes appear.
  // Fallback by HTTP status.
  byStatus: {
    400: 'The request parameters are invalid. Please check them and retry.',
    401: 'Your sign-in session is invalid. Please sign in again.',
    403: 'Access to this operation was denied. Please contact the administrator to grant access.',
    404: 'The resource was not found. Please check that the address is correct.',
    409: 'This operation could not be completed because it conflicts with the current state. Please refresh and retry.',
    429: 'The request was rejected because the rate limit was reached. Please try again shortly.',
    500: 'The server could not complete this operation. Please try again shortly.',
    502: 'The gateway could not reach the backend service. Please try again shortly.',
    503: 'The service is temporarily unavailable. Please try again shortly.',
    504: 'The backend service failed to respond in time. Please try again shortly.',
  },
  network: 'The network could not reach the server. Please check the connection and retry.',
  unknown: 'An unexpected error occurred and the operation could not be completed. Please retry.',
  /**
   * Preferred way to render an API error:
   *   import { resolveApiError } from '@/i18n/useApiError'
   *   resolveApiError(err)  // returns the localized message for the active locale
   * Only data lives here; resolution logic is in useApiError.ts.
   */
}
