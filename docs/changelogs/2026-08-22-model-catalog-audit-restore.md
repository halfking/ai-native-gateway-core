# 2026-08-22 model catalog audit restore

Restores pieces dropped after the credential-scoped models panel landed (`2b55c2357` / `2a74bdf03`):

- Shared `ModelOfferDetailDrawer` omits `context_window` unless the override changed (empty no longer PATCHes `0`).
- IQ history + cross-credential check live in `web/src/components/model/ModelOfferExtrasPanel.vue`.
- `providerLogs` returns `canonical_model` / `provider_model`; LogsTab / NodeDetail chips deep-link to `/models?q=`.
- Credential monitor model rows include `standardized_name` / `canonical_name`; `monitorSummarySchemaVersion` = 8.
- Clear-credential-models response includes `protected_kept`.
