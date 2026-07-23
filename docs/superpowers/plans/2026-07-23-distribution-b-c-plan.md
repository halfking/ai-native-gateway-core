# Plan — Distribution B+C 增量补全

> Companion to [spec](../specs/2026-07-23-distribution-b-c-design.md). Already-approved scope: StorageProvider 接口 + Cloudreve 适配器 + 缺失的 admin/version-check 端点 + 测试 + docs。**不**覆盖 A/D/E。

## Code distribution principle

- **ai-native-maintain** owns 90% of this work: StorageProvider interface, both adapters (LocalFS / Cloudreve), DB schema delta, repository delta, new HTTP endpoints, CLI, E2E tests, docs.
- **llm-gateway-go** owns a thin client adapter: `installer/internal/upgrader/distribution_source.go` reading the new `/maintain-api/upgrade/check` endpoint shape + a few tests. Keep the change <100 LoC.

The maintain service is the source of truth for catalog/admin/version-check; the gateway installer is a thin consumer that we touch only where its current request/response shape mismatches the spec.

---

## Phase 0 — Inventory check (verify nothing else moved while reading)

Status: completed during planning (see spec §6.4 + tool calls).

---

## Phase 1 — DB schema delta

### T01 — Add `status` column to `maintain.releases`

**Files**: `ai-native-maintain/sql/migrations/015_release_status.sql` (new)

```sql
-- 015_release_status.sql
ALTER TABLE maintain.releases
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'draft';
UPDATE maintain.releases
SET status = 'released' WHERE published_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS releases_status_channel_idx
    ON maintain.releases (status, channel);
```

### T02 — Add `storage_uri`, `last_verified_at`, `last_verified_ok` to `maintain.release_artifacts`

**Files**: `ai-native-maintain/sql/migrations/016_release_artifact_storage.sql` (new)

```sql
-- 016_release_artifact_storage.sql
ALTER TABLE maintain.release_artifacts
    ADD COLUMN IF NOT EXISTS storage_uri            TEXT,
    ADD COLUMN IF NOT EXISTS last_verified_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_verified_ok       BOOLEAN,
    ADD COLUMN IF NOT EXISTS storage_kind           TEXT NOT NULL DEFAULT 'local_fs';
CREATE INDEX IF NOT EXISTS release_artifacts_storage_kind_idx
    ON maintain.release_artifacts (storage_kind);
```

### T03 — Verify migrations apply cleanly against 252 pg17 (read-only smoke)

Run:
```bash
cd ai-native-maintain
DATABASE_URL=postgres://maintain@252:5432/maintain?sslmode=disable \
  go run ./cmd/migrate up 2>&1 | tail -20
```

Expected: 015 + 016 applied; `\d maintain.releases` shows `status`; `\d maintain.release_artifacts` shows new columns.

---

## Phase 2 — StorageProvider interface

### T04 — Define `storage.Provider` interface + error vars

**Files**: `ai-native-maintain/internal/distribution/storage/provider.go` (new)

```go
package storage

import (
    "context"
    "errors"
    "io"
    "time"
)

var (
    ErrNotFound = errors.New("storage: not found")
    ErrConflict = errors.New("storage: already exists")
    ErrTooLarge = errors.New("storage: payload too large")
    ErrUnsupported = errors.New("storage: unsupported operation")
)

type Provider interface {
    Kind() string
    EnsurePath(ctx context.Context, dirPath string) error
    Upload(ctx context.Context, opts UploadOptions) (*Artifact, error)
    Head(ctx context.Context, storagePath string) (*Artifact, error)
    Delete(ctx context.Context, storagePath string) error
    Open(ctx context.Context, storagePath string) (io.ReadCloser, error)
    SignedDownloadURL(ctx context.Context, storagePath string, ttl time.Duration) (string, time.Time, error)
    List(ctx context.Context, dirPath string) ([]Listing, error)
}

type UploadOptions struct {
    StoragePath    string
    Source         io.Reader
    Size           int64
    ExpectedSHA256 string
    Overwrite      bool
}

type Artifact struct {
    StoragePath string
    Size        int64
    SHA256      string
    ETag        string
    UpdatedAt   time.Time
}

type Listing struct {
    Name       string
    Size       int64
    SHA256     string
    IsDir      bool
    UpdatedAt  time.Time
}
```

### T05 — Add interface-contract conformance test

**Files**: `ai-native-maintain/internal/distribution/storage/provider_test.go` (new)

```go
//go:build !cloudreve_e2e_skip

package storage_test

import (
    "testing"
    "github.com/<org>/ai-native-maintain/internal/distribution/storage"
)

// Verify a Provider implementation passes conformance.
// Implementations register themselves via storage.Register during init.
func TestProviderConformance(t *testing.T) {
    for _, p := range storage.RegisteredProviders() {
        t.Run(p.Kind(), func(t *testing.T) {
            storage.RunConformanceTests(t, p)
        })
    }
}
```

Conformance test body (`provider_contract.go`):
- `TestEnsurePath_IsIdempotent`: create path twice → both succeed
- `TestUpload_HeadRoundtrip`: upload 1MB random → Head returns matching size + sha256
- `TestUpload_Conflict`: second upload without Overwrite → ErrConflict
- `TestUpload_WithOverwrite`: second upload with Overwrite=true → OK
- `TestOpen_Readback`: upload → Open → io.Copy to sha256 sum matches
- `TestHead_NotFound`: Head on missing → ErrNotFound
- `TestDelete_ThenHead_NotFound`: Delete → Head → ErrNotFound
- `TestSignedDownloadURL_NotEmpty`: returns non-empty URL + Expiry in future

### T06 — `LocalFSAdapter` (wraps existing `cfg.ArtifactRoot`)

**Files**: `ai-native-maintain/internal/distribution/storage/local_fs_adapter.go` (new)

```go
package storage

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "time"
)

type LocalFSAdapter struct { RootDir string }

func NewLocalFSAdapter(root string) *LocalFSAdapter { ... }
func (a *LocalFSAdapter) Kind() string { return "local_fs" }

func (a *LocalFSAdapter) EnsurePath(ctx context.Context, dir string) error {
    full := filepath.Join(a.RootDir, dir)
    return os.MkdirAll(full, 0o755)
}

func (a *LocalFSAdapter) Upload(ctx context.Context, opts UploadOptions) (*Artifact, error) {
    full := filepath.Join(a.RootDir, opts.StoragePath)
    if !opts.Overwrite {
        if _, err := os.Stat(full); err == nil { return nil, ErrConflict }
    }
    if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { return nil, err }
    f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
    if err != nil { return nil, err }
    defer f.Close()
    h := sha256.New()
    n, err := io.Copy(io.MultiWriter(f, h), opts.Source)
    if err != nil { return nil, err }
    sum := hex.EncodeToString(h.Sum(nil))
    if opts.ExpectedSHA256 != "" && sum != opts.ExpectedSHA256 { return nil, fmt.Errorf("sha256 mismatch") }
    return &Artifact{StoragePath: opts.StoragePath, Size: n, SHA256: sum, UpdatedAt: time.Now().UTC()}, nil
}

// Head, Open, Delete, SignedDownloadURL, List follow same pattern.
```

Test file `local_fs_adapter_test.go` covers all 7 methods against a temp dir.

---

## Phase 3 — Cloudreve v4 adapter

### T07 — `CloudreveAdapter` skeleton

**Files**: `ai-native-maintain/internal/distribution/storage/cloudreve_adapter.go` (new)

```go
package storage

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "strings"
    "time"
)

type CloudreveAdapter struct {
    BaseURL    string
    HTTPClient *http.Client
    tokenMu    sync.Mutex
    accessTok  string
    refreshTok string
    tokenExp   time.Time
}

func NewCloudreveAdapter(baseURL string) *CloudreveAdapter {
    return &CloudreveAdapter{
        BaseURL: strings.TrimRight(baseURL, "/"),
        HTTPClient: &http.Client{ Timeout: 30 * time.Second },
    }
}
func (a *CloudreveAdapter) Kind() string { return "cloudreve" }

// login + token refresh + ensureToken() — see below in T08.
```

### T08 — Token management (login + refresh)

```go
type cloudreveLoginResp struct {
    Code int `json:"code"`
    Data struct {
        AccessToken  string `json:"access_token"`
        RefreshToken string `json:"refresh_token"`
        ExpiresAt    time.Time `json:"expires_at"`
    } `json:"data"`
}

func (a *CloudreveAdapter) ensureToken(ctx context.Context) error {
    a.tokenMu.Lock(); defer a.tokenMu.Unlock()
    if a.accessTok != "" && time.Until(a.tokenExp) > 5*time.Minute { return nil }
    // POST /api/v4/session/token with email + password from env.
    req, _ := http.NewRequestWithContext(ctx, "POST",
        a.BaseURL+"/api/v4/session/token",
        strings.NewReader(`{"email":"`+os.Getenv("CLOUDREVE_CI_EMAIL")+
        `","password":"`+os.Getenv("CLOUDREVE_CI_PASSWORD")+`"}`))
    req.Header.Set("Content-Type", "application/json")
    resp, err := a.HTTPClient.Do(req)
    if err != nil { return err }
    defer resp.Body.Close()
    var out cloudreveLoginResp
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return err }
    if out.Code != 0 { return fmt.Errorf("cloudreve login code=%d", out.Code) }
    a.accessTok = out.Data.AccessToken
    a.refreshTok = out.Data.RefreshToken
    a.tokenExp = out.Data.ExpiresAt
    return nil
}

func (a *CloudreveAdapter) doAuthed(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
    if err := a.ensureToken(ctx); err != nil { return nil, err }
    req, _ := http.NewRequestWithContext(ctx, method, a.BaseURL+path, body)
    req.Header.Set("Authorization", "Bearer "+a.accessTok)
    return a.HTTPClient.Do(req)
}
```

### T09 — `EnsurePath` / `Upload` / `Head` / `Delete` / `Open` / `List` / `SignedDownloadURL`

```go
func (a *CloudreveAdapter) EnsurePath(ctx context.Context, dirPath string) error {
    uri := a.toURI(dirPath)
    body := strings.NewReader(fmt.Sprintf(`{"uri":%q,"type":"folder"}`, uri))
    resp, err := a.doAuthed(ctx, "POST", "/api/v4/file/create", body)
    if err != nil { return err }
    defer resp.Body.Close()
    if resp.StatusCode == 409 { return nil }  // already exists
    if resp.StatusCode/100 != 2 { return fmt.Errorf("ensure path: %d", resp.StatusCode) }
    return nil
}

// Upload: POST /api/v4/upload/{sessionId} (prepare) → PUT /api/v4/upload/{sessionId} (bytes) → POST /api/v4/upload/{sessionId}/complete
// Head:   GET  /api/v4/file?uri={uri}  parse parent.metadata.size + sha256
// Delete: DELETE /api/v4/file  body {"uris":[uri]}
// Open:   GET direct URL from Cloudreve file content endpoint; resolve via SignedDownloadURL
// List:   GET /api/v4/file?uri={dirURI}  parse .data.files[]
// SignedDownloadURL: GET /api/v4/file?uri={uri} → extract content URL (Cloudreve exposes direct link in file metadata)
```

Each method must:
- Decode Cloudreve envelope `{code, msg, data}`; treat `code != 0` as error.
- Return `ErrNotFound` on 404 / `code=40401`.
- Return `ErrConflict` on 409 / `code=40901`.
- Retry 3x on 5xx with backoff 1s/2s/4s.

### T10 — `CloudreveAdapter` unit test (httptest fake)

**Files**: `ai-native-maintain/internal/distribution/storage/cloudreve_adapter_test.go` (new)

Fake Cloudreve server (`fakeCloudreve`):
- Route `POST /api/v4/session/token` → return `{"code":0,"data":{"access_token":"t","refresh_token":"r","expires_at":"...+1h"}}`
- Route `POST /api/v4/file/create` → return 200 `{"code":0,"data":{"id":"x"}}`; 409 if exists
- Route `POST /api/v4/upload/{sessionId}` → return 200 with chunk size
- Route `PUT  /api/v4/upload/{sessionId}` → accept body, store in temp dir
- Route `POST /api/v4/upload/{sessionId}/complete` → return 200 with file metadata (size + sha256)
- Route `GET  /api/v4/file` → return Listing or Artifact metadata
- Route `DELETE /api/v4/file` → 200 ok

Tests:
- `TestCloudreve_LoginRoundtrip`
- `TestCloudreve_UploadHeadListDelete`
- `TestCloudreve_TokenRefresh_On401`
- `TestCloudreve_ConflictNotOverwrite`
- `TestCloudreve_OpenReadback_MatchesSHA256`

### T11 — Run conformance test for both adapters

```bash
cd ai-native-maintain
go test ./internal/distribution/storage/...
```

Both `local_fs` and `cloudreve` must pass the conformance test from T05.

---

## Phase 4 — Repository delta

### T12 — Extend `store.Repository` interface

**Files**: `ai-native-maintain/internal/store/repository.go` (modify)

Add methods:
```go
DeleteArtifact(ctx context.Context, releaseID int64, platform, arch, edition string) error
SoftDeleteRelease(ctx context.Context, version string) error
UpdateReleaseMeta(ctx context.Context, version string, fields ReleaseMetaPatch) (*ReleaseMeta, error)
HeadArtifact(ctx context.Context, id int64) (*Artifact, error)
UpdateArtifactVerified(ctx context.Context, id int64, ok bool, at time.Time, sha string, size int64) error
GetReleaseByVersion(ctx context.Context, version string) (*ReleaseMeta, error)
ListAllReleases(ctx context.Context, includeArchived bool) ([]ReleaseMeta, error)
```

Define `ReleaseMetaPatch`:
```go
type ReleaseMetaPatch struct {
    Title       *string
    Description *string
    Changelog   *string
    ImageTag    *string
    ImageDigest *string
    MinVersion  *string
    Mandatory   *bool
}
```

### T13 — Postgres implementation

**Files**: `ai-native-maintain/internal/store/postgres.go` (modify) + new methods file `internal/store/postgres_distribution.go`

Each new method translates to parameterized SQL.

### T14 — Memory implementation (for tests)

**Files**: `ai-native-maintain/internal/store/memory_repository.go` (modify)

Add the same methods against the in-memory map; tests will rely on this.

### T15 — Add unit tests for new repo methods

**Files**: `ai-native-maintain/internal/store/postgres_distribution_test.go` (new, uses testcontainers postgres if available; falls back to memory if not)

---

## Phase 5 — HTTP handlers

### T16 — Refactor `UploadArtifact` handler to use `storage.Provider`

**Files**: `ai-native-maintain/internal/httpapi/upload.go` (modify)

Replace the current `multipart → cfg.ArtifactRoot → insert Artifact` flow with:
1. Parse multipart, get `platform`, `arch`, `edition`, `file`.
2. Compute `storage_path = distribution.BuildArtifactPath(version, platform, arch, edition)` (use existing `ArtifactFileName`).
3. Call `provider.Upload(ctx, UploadOptions{StoragePath: storagePath, Source: file, Size: size, ExpectedSHA256: "", Overwrite: false})`.
4. Call `repo.UpsertArtifact(ctx, Artifact{..., StorageURI: provider.Kind()+":"+storagePath, StorageKind: provider.Kind()})`.
5. Return `{artifact_id, storage_uri, sha256, size}`.

**Behavior change**: existing local-FS deploys keep working (default `Provider = LocalFSAdapter`). Cloudreve deploys set `MAINT_DISTRIBUTION_STORAGE=cloudreve` + env vars.

### T17 — Refactor `FileHandler.ServeArtifact` to use `storage.Provider`

**Files**: `ai-native-maintain/internal/httpapi/file_handler.go` (modify)

If artifact's `storage_kind == "local_fs"` → `provider.Open` + stream.
If artifact's `storage_kind == "cloudreve"` → 302 redirect to `provider.SignedDownloadURL` (TTL 10 min).

### T18 — New `distribution_admin.go` handlers

**Files**: `ai-native-maintain/internal/httpapi/distribution_admin.go` (new)

Endpoints (with proper admin-scope JWT check; re-use existing `RequireAdminScope` middleware pattern from `license_admin.go`):

| Method + path | Handler |
|---|---|
| `GET    /maintain-api/admin/releases?include_archived=` | `ListReleasesHandler` |
| `PUT    /maintain-api/admin/releases/{version}` | `UpdateReleaseHandler` (ReleaseMetaPatch) |
| `DELETE /maintain-api/admin/releases/{version}` | `SoftDeleteReleaseHandler` (sets status='archived') |
| `GET    /maintain-api/admin/releases/{version}/artifacts?verify=` | `ListArtifactsHandler` (when `verify=true`, calls `provider.Head` and updates `last_verified_*`) |
| `DELETE /maintain-api/admin/releases/{version}/artifacts/{platform}/{arch}` | `DeleteArtifactHandler` (calls `provider.Delete` + repo delete) |
| `GET    /maintain-api/upgrade/check?channel=&current=&platform=&arch=&edition=` | `UpgradeCheckHandler` |

`UpgradeCheckHandler` semantics:
- Lookup `releases.channel='stable' AND status='released'` ordered by `published_at DESC LIMIT 1`.
- If `current != ""` and `latest.Version != current` and `autoupdate.IsNewer(current, latest.Version)` → `update_available=true`.
- Return body:
  ```json
  {
    "update_available": true,
    "target": {
      "version": "v1.14.0", "build_seq": 1024, "channel": "stable",
      "git_sha": "...", "release_date": "2026-07-23",
      "min_required": null
    },
    "artifacts": [
      {
        "platform": "linux", "arch": "amd64", "edition": "customer",
        "filename": "llm-gateway-go-v1.14.0-linux-amd64-offline.tar.gz",
        "size": 12345, "sha256": "...",
        "download_url": "https://files.kxpms.cn/...",
        "ticket": "<HMAC ticket>"
      }
    ],
    "release_notes_url": "https://files.kxpms.cn/release/llm-gateway-go/v1.14.0/release-notes.md"
  }
  ```

### T19 — Wire new routes in `server.go`

**Files**: `ai-native-maintain/internal/httpapi/server.go` (modify)

Add the new routes under `adminMux` and `publicMux` per existing pattern.

### T20 — Handler unit tests

**Files**: `ai-native-maintain/internal/httpapi/distribution_admin_test.go` (new)

Use `httptest` + memory repository + fake Cloudreve. Cover:
- `PUT /admin/releases/{v}` with bad body → 400
- `DELETE /admin/releases/{v}` of released version → 409
- `DELETE /admin/releases/{v}` of draft → 200, status archived
- `GET /upgrade/check?current=v0` → update_available=true + correct target
- `GET /upgrade/check?current=` (empty) → 200 with `update_available=false, latest=...`

---

## Phase 6 — Live-ops CLI

### T21 — `cmd/cloudreve-uploader/main.go` (standalone Go CLI)

**Files**: `ai-native-maintain/cmd/cloudreve-uploader/main.go` (new)

Subcommands:
- `cloudreve-uploader upload --base-url=https://files.kxpms.cn --source=foo.tar.gz --dest=release/llm-gateway-go/v1.14.0/foo.tar.gz`
- `cloudreve-uploader head --base-url=... --dest=release/llm-gateway-go/v1.14.0/foo.tar.gz`
- `cloudreve-uploader delete --base-url=... --dest=...`
- `cloudreve-uploader reconcile --base-url=... --prefix=release/llm-gateway-go/v1.14.0/` (lists DB artifacts vs Cloudreve, prints diff)

Credentials: `CLOUDREVE_CI_EMAIL`, `CLOUDREVE_CI_PASSWORD` from env (or `--email-file`, `--password-file` to avoid argv leak).

This CLI re-uses `storage.CloudreveAdapter` directly — no maintain service dependency, callable from packaging / cron / manual ops.

---

## Phase 7 — llm-gateway-go installer adapter

### T22 — `installer/internal/upgrader/distribution_source.go`

**Files**: `llm-gateway-go/installer/internal/upgrader/distribution_source.go` (new)

```go
package upgrader

import (
    "context"
    "encoding/json"
    "net/http"
    "net/url"
)

type DistributionSource struct {
    BaseURL string  // e.g. https://llm.kxpms.cn
}

type UpgradeCheck struct {
    UpdateAvailable bool
    TargetVersion   string
    TargetBuildSeq  int
    Artifacts       []Artifact
    ReleaseNotesURL string
}

type Artifact struct {
    Platform, Arch, Edition, Filename string
    Size                              int64
    SHA256                            string
    DownloadURL, Ticket               string
}

func (s *DistributionSource) Check(ctx context.Context, channel, current, platform, arch string) (*UpgradeCheck, error) {
    u := s.BaseURL + "/maintain-api/upgrade/check?" + url.Values{
        "channel":  {channel},
        "current":  {current},
        "platform": {platform},
        "arch":     {arch},
    }.Encode()
    req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
    resp, err := http.DefaultClient.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    var out UpgradeCheck
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return nil, err }
    return &out, nil
}
```

### T23 — Wire `UpgradeCheck` into `installer upgrade` command

**Files**: `llm-gateway-go/installer/cmd/llm-gw-installer/upgrade.go` (modify)

If a new env var `LLM_GW_DISTRIBUTION_URL` is set, prefer the `DistributionSource.Check` over the existing `license-authority` endpoint. Keep existing behavior as fallback for backward compat. Diff < 30 LoC.

### T24 — Test

**Files**: `llm-gateway-go/installer/internal/upgrader/distribution_source_test.go` (new)

Use `httptest.Server` returning a canned `/maintain-api/upgrade/check` JSON; assert `Check()` parses correctly.

---

## Phase 8 — Tests + E2E

### T25 — Storage conformance (already T11; double-check both adapters pass)

```bash
cd ai-native-maintain && go test ./internal/distribution/storage/...
```

### T26 — Integration test: full HTTP + memory repo + fake Cloudreve

**Files**: `ai-native-maintain/internal/httpapi/distribution_integration_test.go` (new, `//go:build integration`)

End-to-end through httptest:
1. POST /admin/releases → draft
2. POST /admin/releases/{v}/artifacts (multipart, 256KB random) → 200, artifact in DB
3. GET  /admin/releases/{v}/artifacts?verify=true → 200, last_verified_ok=true
4. POST /admin/releases/{v}/publish → status=released
5. GET  /maintain-api/downloads/catalog → contains v
6. GET  /maintain-api/upgrade/check?current=v0.0.1 → update_available=true
7. GET  follow download URL → 200 + correct sha256

### T27 — E2E test against live 252 (opt-in)

**Files**: `ai-native-maintain/e2e/distribution_live_test.go` (new, `//go:build e2e_live`)

Same flow as T26 but points `MAINT_DISTRIBUTION_STORAGE=cloudreve` and `MAINT_DISTRIBUTION_CLOUDREVE_URL=https://files.kxpms.cn`. Cleans up after itself (delete version + artifacts).

Run with:
```bash
go test -tags e2e_live ./e2e/... -run TestE2E_DistributionLive -timeout 5m
```

Expected pass criteria: full flow green against real Cloudreve v4.15.0.

---

## Phase 9 — Docs

### T28 — `docs/distribution/storage-provider.md`

Interface contract, error codes, retry semantics, when to use LocalFS vs Cloudreve.

### T29 — `docs/distribution/cloudreve-adapter.md`

- Account provisioning (create `ci-upload@kxpms.cn`, scope `Publisher`)
- Env vars: `CLOUDREVE_CI_EMAIL`, `CLOUDREVE_CI_PASSWORD`, `CLOUDREVE_BASE_URL`
- SOPS template for `.env.distribution.enc`
- Troubleshooting: 401, 409, 5xx, timeouts

### T30 — `docs/distribution/runbook.md`

- Publish a version
- Unpublish a version
- Delete a version (soft)
- Delete a stuck artifact
- Reconcile DB vs Cloudreve

### T31 — `docs/distribution/e2e.md`

- How to run integration tests
- How to run live E2E
- What to do if `e2e_live` fails on a specific step

### T32 — Update spec status

**Files**: `docs/superpowers/specs/2026-07-23-distribution-b-c-design.md` (modify §0 status line)

Change status from `Draft` to `Implemented — see commit X` once T01–T27 are merged.

---

## Risks / open items at plan-time

| Risk | Mitigation in this plan |
|---|---|
| ci-upload account doesn't exist yet | T11 conformance + T26 integration use fake Cloudreve. T27 live E2E is opt-in. README explicitly notes account must exist before T27. |
| LocalFS refactor in T16/T17 breaks existing installer downloads | Run existing `installer upgrade` smoke after T17; revert if `distribution.Source.Check` regression. |
| `release_artifacts.storage_uri` is NULL for existing rows after T02 | T02 uses `IF NOT EXISTS` so column nullable; existing rows continue to function via old code path; new code branches on `storage_kind`. |
| Cloudreve 5xx mid-upload | T09 retry policy (1s/2s/4s, 3 attempts); T10 test covers at least one 5xx retry. |

---

## Execution

Plan size: **32 tasks** across **9 phases**.

Estimated effort: 3-5 working days for one engineer. Phases 1-7 are sequential; Phase 8 (tests) and Phase 9 (docs) can parallel once Phase 5 lands.

**Next step**: offer the user `executing-plans` (full auto-execute), `subagent-driven-development` (one subagent per task with checkpoints), or `manual` (I do it turn-by-turn with TDD discipline).