// api.ts — v6.0 audit T12 (2026-06-22)
// Barrel re-export. The actual code lives under api/<domain>.ts.
//
// History: this file was a single 4176-line monolith with 117 exported
// functions and ~50 type interfaces. Splitting it into 23 domain
// files makes each surface reviewable on its own, but every existing
// call site uses `import { foo } from '../api'` (or `from '@/api'` if
// the alias were configured), so we keep the public surface stable by
// re-exporting everything here. New code should import from the domain
// module directly: `import { login } from '../api/auth'`.

export * from './api/_core'
export * from './api/auth'
export * from './api/catalog'
export * from './api/providers'
export * from './api/provider-probe'
export * from './api/provider-settings'
export * from './api/keys'
export * from './api/key-applications'
export * from './api/tenants'
export * from './api/usage'
export * from './api/routing'
export * from './api/logs'
export * from './api/models'
export * from './api/system'
export * from './api/free-pool'
export * from './api/compression'
export * from './api/admin'
export * from './api/tuning'
export * from './api/memora'
export * from './api/maas'
export * from './api/settings'
export * from './api/tenant-model-policy'
export * from './api/pending-cache'
export * from './api/session'