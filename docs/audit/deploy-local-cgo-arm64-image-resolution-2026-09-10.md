# Deploy-local CGO arm64 image-resolution audit

**Date:** 2026-09-10  
**Scope:** Apple Silicon failure in the `deploy-local.sh` host-build → container-CGO fallback and shared build-image resolution path.  
**Method:** Static code and contract-test review, plus execution of the two scoped regression harnesses. No production code was changed.

## Executive summary

`deploy-local.sh` correctly detects the host architecture, requests `linux/arm64`, and uses the shared resolver before launching its CGO container build. The current Apple Silicon failure is therefore not an early-bail bug in offline resolution: when only an amd64 offline archive is available, the resolver deliberately rejects it as an architecture mismatch and continues through registry and Docker Hub before failing.

The blocking condition is that the arm64 build requires an arm64-capable build image, but the documented/default offline inventory may contain only an amd64 archive. Diagnostics and regression coverage do not make that situation sufficiently explicit, and the local-deployment guide is stale about the actual CGO fallback.

## Scope

Audited files:

- `scripts/deploy-local.sh` (`build_backend`, especially lines 558–657)
- `scripts/deploy-lib/deploy-image-resolution.sh`
- `scripts/deploy-seamless.sh` (CGO fallback, lines 721–784)
- `Dockerfile`
- `Dockerfile.local-arm64`
- `tests/deploy_local_contract_test.sh`
- `tests/deploy_nocgo_build_test.sh`
- `docs/deployment/local-deployment-test-guide.md`
- Requested `docs/deployment/UNIFICATION-PLAN-2026-09-09.md` (not present in this repository working tree)

The resolver is an SSOT at runtime through `scripts/_shared-lib.sh`, which defaults `AIAN_DEPLOY_LIB` to `~/workspace/ai-native-tools/deploy-lib`. `scripts/deploy-lib` is described as a compatibility symlink/copy; the file examined here is the repository-visible resolver implementation.

## Findings

| ID | Severity | Location | Issue | Recommendation |
|---|---|---|---|---|
| F-01 | P1 | `scripts/deploy-local.sh:588-609`; `scripts/deploy-lib/deploy-image-resolution.sh:143-197` | Apple Silicon CGO fallback cannot proceed with only an amd64 offline archive; the resolver correctly rejects it, but the terminal error does not identify the mismatch or the required corrective artifact. | Add an arm64/amd64-mismatch integration fixture and make the terminal message name the requested platform, observed archive/image architecture, and remediation: provide `*-arm64.tar.{zst,gz}`, publish/pull a multi-arch or arm64 image, or set a suffix-bearing arm64 override. |
| F-02 | P2 | `scripts/deploy-lib/deploy-image-resolution.sh:86-103, 143-164, 205-225` | A non-suffixed `LLM_GATEWAY_BUILD_IMAGE` override has no derived platform variant. Offline and registry lookup may work only if the exact tag/archive is independently multi-arch or already named for arm64. | Validate/document override requirements, or add an explicit `LLM_GATEWAY_BUILD_IMAGE_ARM64`/variant mapping rather than relying on suffix inference. |
| F-03 | P2 | `tests/deploy_local_contract_test.sh`; `tests/deploy_nocgo_build_test.sh` | No test simulates an arm64 host/target where only an amd64 tarball is present, nor tests the resolver's continue-after-mismatch contract. | Add mocked Docker tests that assert: amd64 archive is loaded then rejected, remaining candidates are attempted, and a clear final arm64 remediation is emitted. |
| F-04 | P2 | `docs/deployment/local-deployment-test-guide.md:173-176, 968-976` | The guide says the backend builds with `CGO_ENABLED=0` and lists no CGO image/offline-archive requirements or Apple Silicon failure path. | Update the guide to describe CGO=0 first, container-CGO fallback, `LLM_GATEWAY_BUILD_IMAGE`, required platform image availability, logs, and an Apple Silicon checklist. |
| F-05 | P2 | `docs/deployment/UNIFICATION-PLAN-2026-09-09.md` | The requested unification-plan file is absent from this repository, although code comments cite it as the SSOT design authority. This leaves the repository handoff incomplete. | Restore or link a tracked copy at the cited path, or change references to the canonical location and make that dependency explicit in the local guide. |

No P0 findings were identified. The examined fallback has fail-closed behavior and avoids shipping a stale host artifact.

## Detailed analysis

### F-01 — arm64 fallback needs an arm64-capable build image

#### Flow and current-bug attribution

1. `build_backend` derives `target_arch` from `GOARCH` or `go env GOARCH`; on an Apple Silicon host this is normally `arm64`.
2. It first removes `$RUN_DIR/gateway.build`, then attempts a `CGO_ENABLED=0` build for that target.
3. If that fails, it selects `linux/arm64`, defaults to `kx-base/golang:1.27-alpine-amd64`, and calls `resolve_build_image` before `docker run --platform=linux/arm64`.
4. The resolver treats a cached/loaded `linux/amd64` image as a miss, rather than using emulation to build the arm64 target. That is correct: a native arm64 image supplies an arm64 C compiler suitable for `GOARCH=arm64`.
5. The resolver tries offline archives, configured registry, and Docker Hub. If the only archive contains amd64, it cannot satisfy the `linux/arm64` postcondition, so the full resolution ends in failure.

**Source of current bug:** this is the direct blocker for the reported host condition, but it is a missing compatible image artifact/availability issue rather than an incorrect architecture choice or a resolver early-exit defect. The comments claim arm64 offline tarballs are available; the observed “only amd64 tarball present” state contradicts that deployment prerequisite.

#### Does `_resolve_via_offline_tar` continue after a loaded image architecture mismatch?

Yes. After `docker load`, the resolver tags the embedded reference back to the requested image and calls `_loaded_image_matches`. If its OS/architecture does not equal the requested platform, it prints a warning, then continues the candidate loop at line 191. Once offline candidates are exhausted, `resolve_build_image` proceeds to registry and Docker Hub. It does **not** bail on a loaded-image-architecture mismatch.

#### Is `deploy-local.sh:609` actionable enough?

Partly. It names the requested image/platform and all resolution tiers, but it loses the high-value cause already discovered by the resolver: that an archive/image was amd64 while `linux/arm64` was required. It also does not say which artifact name to obtain (for example, `kx-base-golang-1.27-alpine-arm64.tar.zst`), that a platform-suffixed override may be required, or where resolver/candidate build logs are located. This is a P2 usability defect on a P1 blocker.

#### Edge cases and limitations

- The default image tag is amd64-suffixed even on arm64. This is supported only because `_platform_tag_variant` transforms it to an arm64 candidate. If neither arm64 archive nor arm64 registry tag exists, resolution correctly fails.
- The offline load can replace the requested local tag when tagging the loaded archive. Platform validation prevents accepting an incorrect replacement, but Docker's tag mutation remains observable after a failed attempt.
- Docker Hub fallback can only succeed if the requested tag supports the requested platform.
- `resolve_build_image` sends its normal progress stream to stdout. `build_backend` deliberately redirects it to stderr because the only stdout value must remain the resulting binary pathname captured by `binary=$(build_backend)`.

#### Regression coverage

No regression test covers this host/artefact combination. Existing coverage only greps for the atomic CGO pattern; it does not execute/mock resolver branches.

### F-02 — non-suffixed build-image overrides have no platform derivation

#### Behavior

For `LLM_GATEWAY_BUILD_IMAGE=kx-base/golang:1.27-alpine`, `_platform_tag_variant` returns empty because the tag has no `-amd64`, `-arm64`, or `-aarch64` suffix. The offline candidates are therefore only exact-image filenames (for example, `kx-base-golang-1.27-alpine.tar.gz`); the registry tries only `registry.itestu.cn/lang-base/kx-base-golang:1.27-alpine`; Docker Hub pulls the override with `--platform=linux/arm64`.

This can be usable if the exact image is genuinely multi-architecture, an exact arm64 archive is available under the non-suffixed name, or the configured registry serves an arm64 manifest for that exact tag. It **does not** derive `1.27-alpine-arm64`, so it does not use the current platform-suffixed offline/registry convention. The resolver header explicitly records this known limitation.

#### Source of current bug

Not the source when the default suffixed tag is used. It becomes a likely operator-facing contributor if a non-suffixed override was used, because the arm64 tar/tag will not be discovered through suffix conversion.

#### Regression coverage

None for either exact-tag multi-arch success or non-suffixed override failure.

### F-03 — test coverage is structural, not resolver behavioral

`tests/deploy_local_contract_test.sh` verifies the desired source shape: PID-specific CGO outputs, `install -m 0755` instead of `mv -f`, and stderr-only logging. It does not source/mock `deploy-image-resolution.sh`, fake `docker image inspect/load/pull`, or assert architecture transitions.

`tests/deploy_nocgo_build_test.sh` executes a Linux/arm64 `CGO_ENABLED=0` build, a deliberately broken CGO-only fixture, and dependency graph checks. It validates the reason the fallback might be invoked, not the fallback image-resolution behavior.

#### Executed test result

- `bash tests/deploy_local_contract_test.sh`: **failed early** at its version-collision fixture with return code 64 rather than expected 1. The fixture copies `deploy-local.sh` but its temporary project lacks the full SSOT dependency set needed by the script; this failure occurs before its later CGO assertions.
- `bash tests/deploy_nocgo_build_test.sh`: **failed**. Its initial CGO=0 arm64 build and dependency graph fail because `github.com/yalue/onnxruntime_go` has no CGO-disabled Go files. The scratch regression/control tests pass. This result confirms that the host build is expected to enter the CGO fallback in this checkout, but also shows the test's stated “CGO=0 must succeed” contract is stale relative to the current dependency graph.

Neither test failure was modified during this audit.

### F-04 — local deployment guide is stale for the actual build state machine

The guide describes step 4 as an unconditional `CGO_ENABLED=0` backend build and gives generic Docker prerequisites. It does not document that CGO=0 can fail due to ONNX/SQLite-related dependencies, that deploy-local then resolves a platform-specific builder image, that `LLM_GATEWAY_BUILD_IMAGE` controls it, or that Apple Silicon must have an arm64 image source available.

**Source of current bug:** not executable root cause, but it substantially delays diagnosis because it promises a path which no longer describes actual operation.

**Regression coverage:** no documentation consistency check exists.

### F-05 — cited unification plan is missing locally

The requested `docs/deployment/UNIFICATION-PLAN-2026-09-09.md` does not exist under this repository or the parent `/Users/xutaohuang/workspace/official-deploy` tree at audit time. The resolver, shared-library loader, and CGO fallback comments cite it as the authoritative SSOT plan. The only authoritative copy may be outside this checkout, but the location was not available to the audit.

**Source of current bug:** not a runtime source. It is a handoff and maintenance risk: an operator cannot validate intended arch/SSOT behavior from tracked project documentation.

## Other scoped components

### `scripts/deploy-local.sh` host-build state isolation

The host attempt does **not** leak build environment settings to the CGO fallback. `CGO_ENABLED`, `GOOS`, and `GOARCH` for the host build are assignment-scoped to its subshell. The fallback creates its own Docker environment with `CGO_ENABLED=1`, `GOOS=linux`, and the same derived target architecture.

It removes `$RUN_DIR/gateway.build` before the host build, preventing a failed build from being mistaken for an old artifact. A failed host compile leaves `build-host.log` intentionally as diagnostics and may populate the normal Go cache, but it cannot leave a successful output at `$out`; the fallback either installs a validated non-empty output or fails. The fallback may encounter unrelated stale files in `.build-local`, but it uses a per-process `gateway.build.$$` name, checks that exact output is non-empty, cleans it on both failure path and success, and does not consume arbitrary files from that directory.

Known residual limitation: a process killed hard enough to preserve an old per-PID file could theoretically collide after PID reuse. The subsequent container build overwrites the same target and the non-empty check limits the effect; explicit `mktemp` would remove even this extremely low-probability residue concern.

### Atomic-build pattern

The required pattern is preserved in deploy-local:

- Per-process staging path: `.build-local/gateway.build.$$`.
- The container writes and ownership-fixes exactly that staging file.
- The script checks `[[ -s "$cgo_out" ]]` before publication.
- `install -m 0755 "$cgo_out" "$out"` publishes it rather than `mv -f`; `install` failure is checked and reported.

The contract test contains static checks for this pattern, although its earlier fixture failure currently prevents the test from reaching them in a full run.

### `scripts/deploy-seamless.sh` is amd64-only and unaffected by local arm64 selection

Confirmed. Its initial build pins `GOOS=linux GOARCH=amd64`, resolution always requests `linux/amd64`, Docker always runs `--platform linux/amd64`, and the fallback output remains an amd64 static-musl binary for the remote host. An Apple Silicon workstation can use Docker emulation to perform this remote-target build, but it never changes seamless's target to arm64. Its current issue surface is image availability for amd64, not the deploy-local arm64 missing-artifact condition.

It retains the same per-PID output and `install`, although its error logging is less detailed than local's (no dedicated CGO stderr log capture).

### Dockerfiles are alternate paths, not invoked by `deploy-local.sh`

- `Dockerfile` builds a complete image with CGO enabled (`gcc`, `musl-dev`) and uses `registry.kxpms.cn/kx-base` images. It is an independent Docker build path.
- `Dockerfile.local-arm64` assumes a prebuilt host-produced arm64 binary in `.build-local/llm-gateway-go`, then packages it in a slim runtime. It is also an independent build path and requires a host Linux arm64 CGO toolchain.
- `deploy-local.sh` does not invoke either file. In Docker runtime mode it dynamically writes `$RUN_DIR/runtime.Dockerfile`, which copies the already staged `gateway` and web files into `LLM_GATEWAY_RUNTIME_IMAGE` (default `alpine:3.22`). Thus neither repository Dockerfile can remedy the failed builder-image resolution without an explicit separate workflow.

## Test coverage matrix

| Scenario / contract | Existing coverage | Result / gap |
|---|---|---|
| CGO=0 Linux arm64 gateway build | `tests/deploy_nocgo_build_test.sh:70-99` | Present; currently fails because `onnxruntime_go` is CGO-only. |
| CGO-only regression fixture fails when CGO disabled | `tests/deploy_nocgo_build_test.sh:108-165` | Present; passes. |
| CGO=0 dependency graph excludes SQLite CGO files | `tests/deploy_nocgo_build_test.sh:173-205` | Present; currently fails earlier due to `onnxruntime_go`. |
| Local/per-PID output + install-not-mv source shape | `tests/deploy_local_contract_test.sh:549-581` | Present as grep contract; full harness currently stops earlier on an SSOT fixture failure. |
| stdout isolation from `binary=$(build_backend)` | `tests/deploy_local_contract_test.sh:583-596` | Present as grep contract; not reached in failed full run. |
| Arm64 target, only amd64 offline tar present | None | Missing. |
| Continue after loaded-image architecture mismatch | None | Missing. Static implementation does continue. |
| arm64 offline archive selected before amd64 fallback candidate | None | Missing. |
| Non-suffixed image override behavior | None | Missing. |
| Final diagnostic includes requested/observed architectures and corrective archive/tag | None | Missing. |
| Dockerfile paths are absent from deploy-local call graph | None | Could be a low-cost grep/source test. |
| Documentation matches CGO fallback behavior | None | Missing. |

## Recommended follow-up actions

1. **P1 — make the required arm64 builder image available and validate it before retrying.** Publish/load `kx-base/golang:1.27-alpine-arm64` (or an equivalent exact arm64/multi-arch override) in one configured resolver tier. Confirm `docker image inspect <requested-tag> --format '{{.Os}}/{{.Architecture}}'` reports `linux/arm64`.
2. **P1 — add a resolver integration/unit shell test with mocked Docker.** Simulate `linux/arm64` target plus only `*-amd64.tar.gz`, assert loaded mismatch does not return success, assert all remaining tiers are tried, and assert final failure makes the arm64 artifact requirement explicit.
3. **P2 — improve the deploy-local terminal error.** Include `target_arch`, requested platform, attempted/observed archive architecture where available, exact expected tar names, `LLM_GATEWAY_BUILD_IMAGE` override guidance, and `$RUN_DIR/build-host.log`/resolver output locations.
4. **P2 — decide and document override policy.** Either require platform-suffixed overrides for per-arch artifacts or introduce explicit per-platform image variables. Test non-suffixed multi-arch and single-arch cases.
5. **P2 — repair scoped test harnesses.** Make `deploy_local_contract_test.sh` provision the complete shared-library fixture or isolate its source checks; update/split `deploy_nocgo_build_test.sh` so CGO-only ONNX expectations match the intended current fallback design.
6. **P2 — update and restore documentation.** Correct the local deployment guide, add Apple Silicon/offline troubleshooting, and restore or link the cited unification plan from a tracked canonical path.
7. **P3 — consider replacing `$$` with `mktemp` for cgo staging names.** Current behavior is robust for concurrent live deployments; `mktemp` would also eliminate the PID-reuse residue edge case.

## Appendix — relevant code snippets

### A. Host build and target-platform selection

`scripts/deploy-local.sh:558-598`

```bash
local out="$RUN_DIR/gateway.build"
local target_os="${GOOS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
local target_arch="${GOARCH:-$(go env GOARCH)}"
(( DL_DOCKER )) && target_os=linux
rm -f "$out"
if ! (cd "$PROJECT_ROOT" && CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build ... -o "$out" ./cmd/gateway) 2>"$RUN_DIR/build-host.log"; then
  ...
  if [[ "$target_arch" == "arm64" || "$target_arch" == "aarch64" ]]; then
    docker_platform="linux/arm64"
  elif [[ "$target_arch" == "amd64" || "$target_arch" == "x86_64" ]]; then
    docker_platform="linux/amd64"
  else
    die "unsupported target architecture: $target_arch (expected arm64 or amd64)"
  fi
```

### B. Resolver invocation and current terminal diagnostic

`scripts/deploy-local.sh:599-610`

```bash
if ! resolve_build_image "$build_image" "$docker_platform" >&2; then
  die "CGO fallback needs image $build_image ($docker_platform); tried local cache, offline tar in ~/work/{docker-base-images,docker-base-image}/lang-base/, registry.itestu.cn/lang-base/kx-base-golang and docker hub"
fi
```

### C. Continue after offline archive architecture mismatch

`scripts/deploy-lib/deploy-image-resolution.sh:172-197`

```bash
if _load_offline_tar "$tar_path"; then
  local embedded="$DEPLOY_IMAGE_TAR_REF:${image##*:}"
  if [[ "$embedded" != "$image" ]]; then
    docker tag "$embedded" "$image" 2>/dev/null || true
  fi
  if [[ -n "$ptag" && "$embedded" == "$image" ]]; then
    docker tag "$DEPLOY_IMAGE_TAR_REF:$ptag" "$image" 2>/dev/null || true
  fi
  if _loaded_image_matches "$image" "$platform"; then
    return 0
  fi
  _deploy_image_warn "tar $tar_name did not yield a usable $image; trying next candidate"
fi
...
return 1
```

### D. Platform suffix derivation and non-suffixed limitation

`scripts/deploy-lib/deploy-image-resolution.sh:90-103`

```bash
local image_tag=${image##*:}
local arch=${platform##*/}
local base="$image_tag"
case "$base" in
  *-amd64)   base="${base%-amd64}" ;;
  *-arm64)   base="${base%-arm64}" ;;
  *-aarch64) base="${base%-aarch64}" ;;
  *) echo ""; return 0 ;;
esac
if [[ "$base-$arch" != "$image_tag" ]]; then echo "$base-$arch"; else echo ""; fi
```

### E. Atomic local CGO publication

`scripts/deploy-local.sh:611-653`

```bash
local cgo_out="$PROJECT_ROOT/.build-local/gateway.build.$$"
...
if [[ ! -s "$cgo_out" ]]; then
  ...
  die "backend CGO build produced no output at $cgo_out ..."
fi
if ! install -m 0755 "$cgo_out" "$out" 2>"$RUN_DIR/build-cgo-mv.log"; then
  ...
  die "failed to install $cgo_out -> $out ..."
fi
rm -f "$cgo_out"
```

### F. Seamless target is explicitly amd64

`scripts/deploy-seamless.sh:732-756, 764-771`

```bash
if ! CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ...; then
  ...
  if ! resolve_build_image "$build_image" "linux/amd64"; then ... fi
  ...
  docker run --rm --platform linux/amd64 \
    ... -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=amd64 \
```

### G. Runtime Dockerfile generated by deploy-local, rather than repository Dockerfiles

`scripts/deploy-local.sh:759-771`

```bash
local image="${LLM_GATEWAY_RUNTIME_IMAGE:-alpine:3.22}" image_file="$RUN_DIR/runtime.Dockerfile"
cat > "$image_file" <<'EOF'
ARG BASE_IMAGE=alpine:3.22
FROM ${BASE_IMAGE}
COPY gateway /opt/llm-gateway-go/gateway
COPY web /opt/llm-gateway-go/web
COPY version.json /opt/llm-gateway-go/version.json
...
EOF
docker build -q --build-arg "BASE_IMAGE=$image" -f "$image_file" -t "kx-llm-gateway-local:${RELEASE_VERSION}" "$bundle" >/dev/null
```
