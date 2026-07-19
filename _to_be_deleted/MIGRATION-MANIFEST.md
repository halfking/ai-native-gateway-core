# _to_be_deleted Migration Manifest

> Records code retired from the Gateway as ownership moves to
> `ai-native-maintain`. Each entry below is a rollback snapshot — the code
> is kept verbatim so a failed cutover can restore the Gateway-side handler
> without reconstructing it. See `docs/优化v1/06-旧仓待删除与回滚.md`.

## distribution-v1

- **Retired**: 2026-07-20 (B1 of the maintain migration)
- **Source path**: `distribution/`
- **Snapshot path**: `_to_be_deleted/distribution-v1/`
- **Canonical owner**: `ai-native-maintain/internal/distribution` + `internal/httpapi/{file_handler,publish,server}.go`
- **What moved**:
  - Catalog service (multi-version aggregation) → maintain `internal/distribution/catalog.go`
  - Public download/donation API + ticket + file handler → maintain `internal/httpapi` + `internal/distribution/{ticket,checksum}.go`
  - Publish admin API → maintain `internal/httpapi/publish.go`
- **What stayed on the Gateway**: nothing — the whole package moved.
- **Wiring removed**: `cmd/gateway/main.go` Phase 8 block (dist* construction
  + Echo route registration) and the four `mux.Handle` mounts for
  `/api/downloads/`, `/api/donations/`, `/api/public/offline-activation/`,
  `/llm-gateway-go/`. These paths are now served by the
  `newMaintainGatewayHandler` reverse proxy (B0), which tags the legacy
  `/api/*` prefixes with `Deprecation: true`.
- **Not migrated to maintain (intentional)**:
  - `LicenseHolder` / `DownloadStats` / `EnsureHolderByEmail` — deferred to B5
    (donation/stats).
  - `maas.PaymentProvider` wiring — maintain uses the B5 static-QR donation
    model, not the Gateway stub provider.
  - Gateway `TicketClaims` short field names (`rid/ver/plt`) — maintain keeps
    its own claims shape to avoid invalidating already-issued tickets.
- **Rollback**:
  1. `git mv _to_be_deleted/distribution-v1 distribution`
  2. Restore the Phase 8 block + four `mux.Handle` lines in `cmd/gateway/main.go`
     from the commit that retired them.
  3. Unset `MAINTAIN_SERVICE_URL` so the Gateway stops proxying to maintain.
  4. `go build ./cmd/gateway` — the distribution import path is unchanged.
- **Delete-by**: do not delete until B1 has passed its staging gate (catalog
  multi-version, ticket→download, checksum, path-traversal negative tests)
  and one observation window has elapsed with no rollback.
