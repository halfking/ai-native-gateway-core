# Guest UI refactor — unified header & deploy flow

**Date:** 2026-07-15
**Target:** 245 preprod
**Scope:** Frontend only (no DB migrations)

## Summary

Refactored unauthenticated UI: larger nav branding, deploy/install/activate
flow on landing page, unified guest header across public routes, fixed
spurious login modal on `/` and `/download`.

## Files

- `web/src/components/GuestHeader.vue` (new)
- `web/src/components/DeployFlowSection.vue` (new)
- `web/src/App.vue`, `LandingView.vue`, `PublicPortalLayout.vue`
- `web/src/views/public/DownloadView.vue`
- `web/src/locales/zh-CN|en-US/landing.ts`, `public.ts`
