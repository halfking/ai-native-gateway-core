// api/index.ts — barrel re-export so Vue files can do
//   `import { foo, bar } from '../api'`
// and pick up functions/Types defined across providers.ts, provider-probe.ts,
// provider-settings.ts, _core.ts, and the rest of the api/ directory.
//
// 2026-07-04 (v738 port): the source project uses this barrel pattern; the
// current project's pre-existing Views never imported from '../api' so
// there was no index.ts. Adding this file lets the new copied Vue pages
// resolve their named imports without listing each sub-module.

export * from './providers'
export * from './provider-probe'
export * from './provider-settings'
export {
  BASE,
  headers,
  req,
  store,
  clearApiKey,
  clearAll,
  authBearer,
  getLocale,
  type UserInfo,
} from './_core'

// catalog and system helpers that the providers page also references
export { getCatalog, type CatalogEntry } from './catalog'
export { getBackgroundTasksStatus, type BackgroundTasksStatus } from './system'
