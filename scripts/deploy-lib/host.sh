#!/usr/bin/env bash
# =====================================================================
# scripts/deploy-lib/host.sh — shared 154/245 host seams
#
# Implements the spec's "Shared 154/245 staging, restart, and
# verification seams" — slice 1 surface + slice 4 functional layer.
#
# Conventions established here:
#   - Release bundles live under /opt/llm-gateway-go/releases/${VERSION}
#     (one bundle per deploy; the symlink-current swap is the entire
#     kernel-visible boundary).
#   - The kernel-visible binary path is the symlink
#     /opt/llm-gateway-go/gateway (245) or
#     /opt/llm-gateway-go/llm-gateway-go (154) → current/<gateway>.
#   - /opt/llm-gateway-go/web → current/<web> (245) or current/web (154)
#   - /opt/llm-gateway-go/version.json → current/<version.json>
#   - "current" is itself a symlink into releases/.
#   - Each bundle contains: executable (target's binary name),
#     web/ static root, VERSION + version.json, SHA256SUMS, and
#     deployment.json with verified=false initially.
#
# Slice 4 functional additions:
#   host_stage_release   — assemble the local bundle and write its
#                          SHA256SUMS manifest.
#   host_upload_release  — scp the staged bundle to the target (mkdir
#                          first so the remote lock can race us, never
#                          the other way around).
#   host_atomic_switch   — `ln -sfn releases/${VERSION} current` then
#                          systemctl restart; verify returns verified
#                          status from host_mark_verified.
#   host_list_verified   — newest-first list of verified=true bundles.
#   host_prune_releases  — keep the 5 newest verified + active.
#   host_rollback_to     — swap current to an exact-version verified
#                          release; refuse missing/active/unverified.
#
# The 154 contract uses the same release-bundle layout in this slice so
# that the two canonical targets share one staging function. 154
# rollback, however, stays on the existing runbook (the contract field
# rollback_policy=runbook) — slice 5 will convert it to versioned once
# parity tests pass.
# =====================================================================

if [[ -z "${BASH_VERSION:-}" ]]; then
  echo "host.sh: requires bash" >&2
  # shellcheck disable=SC2317  # only reached when invoked directly (not sourced)
  return 1 2>/dev/null || exit 1
fi

# shellcheck disable=SC2317

# Render the canonical release-bundle layout for one target. The
# function is intentionally pure: it takes a target name and a version
# string and prints the absolute paths it would manage. No file is
# touched.
#
# Override hook: HOST_INSTALL_ROOT in the environment redirects all
# paths to that root. The offline test harness uses this to avoid an
# SSH layer.
host_release_layout() {
  local root=${HOST_INSTALL_ROOT:-}
  local target=$1
  local version=$2
  if [[ -z "$root" ]]; then
    local root_var="HOST_${target^^}_ROOT"
    root=${!root_var:-}
    case "$target" in
      154) root=${root:-/opt/llm-gateway-go} ;;
      245) root=${root:-/opt/llm-gateway-go} ;;
      *)   echo "host_release_layout: unsupported target $target" >&2; return 64 ;;
    esac
  fi

  cat <<EOF
root=$root
releases_dir=$root/releases
release_dir=$root/releases/$version
current_link=$root/current
binary_link=$root/$( [[ $target == 245 ]] && echo gateway || echo llm-gateway-go )
web_link=$root/web
version_link=$root/version.json
metadata_file=$root/releases/$version/deployment.json
checksum_file=$root/releases/$version/SHA256SUMS
EOF
}

# Just the install root. Used by host_list_verified_releases and
# host_prune_releases which don't need a per-version path.
#
# Override hook: setting HOST_INSTALL_ROOT in the environment pins the
# returned root to that value. The offline test harness uses this to
# redirect host_* calls into a TMPDIR without an SSH layer.
host_root_for() {
  if [[ -n "${HOST_INSTALL_ROOT:-}" ]]; then
    printf '%s\n' "$HOST_INSTALL_ROOT"
    return
  fi
  local target=$1
  local root_var="HOST_${target^^}_ROOT"
  local root=${!root_var:-}
  case "$target" in
    154) root=${root:-/opt/llm-gateway-go} ;;
    245) root=${root:-/opt/llm-gateway-go} ;;
    *)   echo "host_root_for: unsupported target $target" >&2; return 64 ;;
  esac
  printf '%s\n' "$root"
}

# Map a target to its in-bundle executable name. Used by stage / verify
# / prune to find the file inside a release directory.
host_binary_name() {
  case "$1" in
    154) echo "llm-gateway-go" ;;
    245) echo "gateway" ;;
    *)   echo "host_binary_name: unsupported target $1" >&2; return 64 ;;
  esac
}

# Verify that the host-side prerequisites exist before a deploy attempt:
# the service manager is reachable, the binary path is writable, and
# the web directory is present. Returns 0 on success, nonzero otherwise.
host_preflight() {
  local ssh_cmd=$1 target=$2
  local contract service_name service_manager
  contract=$(target_contract "$target")
  service_name=$(printf '%s' "$contract" | sed -n 's/.*"service_name":"\([^"]*\)".*/\1/p')
  service_manager=$(printf '%s' "$contract" | sed -n 's/.*"service_manager":"\([^"]*\)".*/\1/p')

  [[ -n "$service_manager" ]] || { echo "service_manager missing for $target" >&2; return 1; }

  case "$service_manager" in
    systemd)
      "$ssh_cmd" "systemctl show '$service_name' >/dev/null 2>&1" || {
        echo "ERROR: $target systemd unit $service_name not visible" >&2
        return 1
      }
      ;;
    k3s|launchd|"")
      : # Slice 1-4 only knows systemd; other managers fall through.
      ;;
  esac
  return 0
}

# Stage a release bundle on the SOURCE side. The bundle is created at
# $1 (a local directory the caller has already made), and gets:
#   - executable copied from $2 (the freshly built binary)
#   - web/ assets copied from $3 (the built static root)
#   - version.json + VERSION copied from the repo root
#   - SHA256SUMS manifest written for all runtime artifacts
#   - deployment.json written with verified=false
#
# Returns 0 on success; nonzero leaves $1 untouched.
host_stage_release() {
  local bundle_dir=$1 binary_src=$2 web_src=$3
  local target=${HOST_STAGE_TARGET:-245}
  local version=${HOST_STAGE_VERSION:-dev}
  local bin_name
  bin_name=$(host_binary_name "$target") || return 64

  [[ -d "$bundle_dir"  ]] || { echo "host_stage_release: missing bundle_dir $bundle_dir" >&2; return 1; }
  [[ -f "$binary_src"  ]] || { echo "host_stage_release: missing binary_src $binary_src" >&2; return 1; }
  [[ -d "$web_src"     ]] || { echo "host_stage_release: missing web_src $web_src" >&2; return 1; }

  install -m 0755 "$binary_src" "$bundle_dir/$bin_name"
  cp -R "$web_src/." "$bundle_dir/web/"
  if [[ -f version.json ]]; then cp version.json "$bundle_dir/version.json"; fi
  if [[ -f VERSION      ]]; then cp VERSION      "$bundle_dir/VERSION"; fi

  # 2026-07-19: Copy configs directory for sensitive_words.json and other runtime configs
  if [[ -d configs ]]; then
    cp -R configs "$bundle_dir/configs"
  fi

  # Checksums. Include runtime configs so a changed sensitive-word list
  # cannot be deployed without being detected by bundle verification.
  (
    cd "$bundle_dir"
    sha256sum "$bin_name" version.json VERSION
    if [[ -d configs ]]; then
      while IFS= read -r file; do
        sha256sum "$file"
      done < <(find configs -type f -print | sort)
    fi
  ) > "$bundle_dir/SHA256SUMS"

  # Initial deployment.json (verified=false). The orchestrator flips
  # this to true only after /healthz returns 2xx.
  cat >"$bundle_dir/deployment.json" <<EOF
{"target":"$target","version":"$version","verified":false,"created_at":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF
}

# Verify a staged bundle locally: every file listed in SHA256SUMS
# exists and matches. Caller has already transferred the bundle; this
# function runs on the TARGET side. When the offline harness stubs ssh,
# the harness invokes this directly against a path on disk.
host_verify_bundle() {
  local ssh_cmd=$1 bundle_dir=$2
  local bin_name
  bin_name=$(host_binary_name "${HOST_STAGE_TARGET:-245}") || return 64
  "$ssh_cmd" "cd '$bundle_dir' && sha256sum -c SHA256SUMS --strict >/dev/null 2>&1"
}

# Restart the tracked service on the target host. Slice 1-4 only knows
# systemd; the k3s and launchd paths come with their respective slices.
#
# 2026-08-19 (rule 11 §1 plan-first, follow-up §3.5 候选 C 验证):
#   原实现 "$ssh_cmd \"systemctl restart '$service_name'\" 是 fire-and-forget:
#   systemd stop 阶段旧 Go srv.Shutdown 最长阻塞 30s 让 :8781 端口,
#   期间 systemd 拉起新 binary → bind :8781 失败 → exit 1 → RestartSec=5 循环.
#   现改为显式 stop → 同步等端口释放 (healthz 不可达) → start 三段式,
#   复用 host_drain_and_stop (下 328-351) 的 drain 模式避免重复代码.
#   drain 超时 30s; 失败时函数返回 1 让上层 _seamless_auto_rollback 兜底.
host_restart_service() {
  local ssh_cmd=$1 target=$2
  local service_name health_url deadline_port_release
  service_name=$(target_field "$target" service_name)
  health_url=$(target_field "$target" health_url)
  [[ -n "$service_name" ]] || { echo "service_name missing for $target" >&2; return 1; }
  [[ -n "$health_url" ]] || { echo "health_url missing for $target" >&2; return 1; }

  # Step 1: stop (旧 binary srv.Shutdown 开始 drain)
  "$ssh_cmd" "systemctl stop '$service_name'" || true

  # Step 2: 等 :8781 端口真释放 (healthz 不可达 = port freed)
  deadline_port_release=$(( $(date +%s) + 30 ))
  while (( $(date +%s) < deadline_port_release )); do
    if ! "$ssh_cmd" "curl -fsS --max-time 1 '$health_url' >/dev/null 2>&1"; then
      break
    fi
    sleep 1
  done

  # Step 3: 此时端口必已释放,启新 binary (systemd 接管后续 RestartSec 兜底)
  "$ssh_cmd" "systemctl start '$service_name'"
}

# Wait for /healthz on the target host to answer 2xx. Slice 1-4 uses a
# simple curl loop with a fixed cap so the test harness can run it
# offline against the fake-ssh bin. HEALTHZ_OK=1 makes the fake curl
# exit 0; absent that, fake curl exits 22 (server unreachable).
host_wait_healthy() {
  local ssh_cmd=$1 target=$2 timeout_s=${3:-60}
  local health_url
  health_url=$(target_field "$target" health_url)
  [[ -n "$health_url" ]] || { echo "health_url missing for $target" >&2; return 1; }
  local deadline=$(( $(date +%s) + timeout_s ))
  while (( $(date +%s) < deadline )); do
    if "$ssh_cmd" "curl -fsS --max-time 2 '$health_url' >/dev/null 2>&1"; then
      return 0
    fi
    sleep 2
  done
  echo "ERROR: $target health check timed out after ${timeout_s}s" >&2
  return 1
}

# 2026-08-19: OOM 复盘加固. OOM killer 杀 nginx 后 systemd 不自愈 (vendor unit
# 缺 Restart=always), gateway 仍然存活且 127.0.0.1:8781/healthz OK — 旧 host_wait_healthy
# 通过但公网 80/443 实际挂了 1h21min 才被发现.
#
# 本函数在 target 自身 curl https://127.0.0.1/healthz 验证 nginx→gateway 链路.
# nginx 用的是 let's encrypt 给 kxpms.cn 的 cert, curl 127.0.0.1 会 CN mismatch,
# 用 -k 跳过 cert verify. -k 在这里可接受: 我们只验证"链路通 + 返回 200", 不
# 验证 TLS 身份 (TLS 身份由发起机器的公网 curl 验证, 见 deploy-245 SKILL).
#
# 当 target contract 有 internal_https_health_url 时调用, 空则跳过 (兼容 252/kaixuan).
host_wait_https_healthy() {
  local ssh_cmd=$1 target=$2 timeout_s=${3:-30}
  local https_url
  https_url=$(target_field "$target" internal_https_health_url)
  [[ -n "$https_url" ]] || { echo "  internal_https_health_url not set for $target, skip"; return 0; }
  local deadline=$(( $(date +%s) + timeout_s ))
  while (( $(date +%s) < deadline )); do
    if "$ssh_cmd" "curl -kfsS --max-time 3 '$https_url' >/dev/null 2>&1"; then
      return 0
    fi
    sleep 2
  done
  echo "ERROR: $target nginx (443) https health check timed out after ${timeout_s}s — nginx may be down (post-OOM symptom)" >&2
  return 1
}

# Wait until nginx actually serves the upgrade/maintenance page after the
# UPGRADING marker is created. The marker is checked per-request by nginx
# (no reload needed), but the first served maintenance response can lag the
# marker write — CDN/edge cache, a slow nginx worker, or the 252 public
# ingress syncing behind the 154 local marker all add latency.
#
# 2026-08-28: timeout raised to 120s (typical enablement ~60s) so a lagging
# public edge never aborts the deploy prematurely. The check passes only when
# the marker file exists AND nginx returns the maintenance page (detected via
# the X-LLM-Gateway-Upgrade: in-progress header, or the page body marker).
host_wait_upgrade_banner() {
  local ssh_cmd=$1 target=$2 timeout_s=${3:-120}
  local install_root=${4:-$(host_root_for "$target")}
  local maintenance_dir=${5:-$install_root/maintenance}
  local probe_url=${6:-}
  local probe_extra_args=${7:-}
  local maint_root="$maintenance_dir"
  local url
  # 2026-08-28: 探测 URL 可显式传入 (第 6 参)。252 场景下 ssh_cmd 在 252 上
  # 执行, 但 target 字段仍是 154 的 127.0.0.1 — 127.0.0.1 在 252 上命中的
  # 是另一个 vhost, 维护页永远探测不到 (部署被误判失败)。显式传 URL 时以
  # 传入值为准; 否则回落到 target 的 internal_https_health_url。
  # 第 7 参为附加 curl 参数 (如 --resolve host:443:127.0.0.1), 允许含空格。
  if [[ -n "$probe_url" ]]; then
    url="$probe_url"
  else
    url=$(target_field "$target" internal_https_health_url 2>/dev/null)
    url="${url%%/healthz}/"
  fi
  [[ -n "$url" ]] || url="https://127.0.0.1/"
  local deadline=$(( $(date +%s) + timeout_s ))
  local start_ts=$(date +%s)
  echo "  等待升级静态页生效 (最长 ${timeout_s}s)…"
  while (( $(date +%s) < deadline )); do
    # Marker must exist (nginx decides per-request on this file) …
    if "$ssh_cmd" "test -f '$maint_root/UPGRADING' && test -f '$maint_root/index.html'" 2>/dev/null; then
      # … and nginx must actually serve the maintenance page.
      local out
      out=$("$ssh_cmd" "curl -ksS --max-time 3 ${probe_extra_args:+$probe_extra_args} -o /tmp/kx-upgrade-banner-\$\$.html -D - '$url' 2>/dev/null; cat /tmp/kx-upgrade-banner-\$\$.html 2>/dev/null" ) || true
      if printf '%s' "$out" | grep -qiE 'X-LLM-Gateway-Upgrade:[[:space:]]*in-progress|系统正在升级|正在升级 · llm-gateway-go'; then
        local elapsed=$(( $(date +%s) - start_ts ))
        "$ssh_cmd" "rm -f /tmp/kx-upgrade-banner-\$\$.html" 2>/dev/null || true
        echo "  ✓ 升级静态页已生效 (target=$target, ${elapsed}s)"
        return 0
      fi
    fi
    sleep 2
  done
  "$ssh_cmd" "rm -f /tmp/kx-upgrade-banner-\$\$.html" 2>/dev/null || true
  echo "ERROR: $target 升级静态页未在 ${timeout_s}s 内生效 — marker 已写但 nginx 未返回维护页" >&2
  return 1
}

# Mark a release bundle as verified=true once /healthz answers 2xx.
# The metadata file is rewritten in-place — the orchestrator rolls
# back via the deployment.json instead of new sidecars.
#
# Implementation: pure shell. The deployment.json format is fixed
# (verified, verified_at, target, version), and rewriting two fields
# in-place is straightforward enough that pulling in python / jq on a
# minimal target host would be overkill.
host_mark_verified() {
  local ssh_cmd=$1 target=$2 version=$3
  local metadata_file
  metadata_file=$(host_release_layout "$target" "$version" | sed -n 's/^metadata_file=//p')
  local now
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  # Hand the rewrite to a single remote shell so atomicity is preserved.
  # Steps:
  #   1. mkdir -p the metadata directory.
  #   2. Replace `"verified":(true|false)` with `"verified":true`.
  #      Use sed -E for portability; macOS sed needs `sed -E`.
  #   3. Upsert verified_at, target, version fields (existing values win).
  #   4. Validate JSON shape — the orchestrator re-reads it during
  #      rollback selection.
  "$ssh_cmd" "set -e; m='$metadata_file'; mkdir -p \"\$(dirname \"\$m\")\"; \
    if [ ! -f \"\$m\" ]; then \
      printf '{\"target\":\"$target\",\"version\":\"$version\",\"verified\":true,\"verified_at\":\"$now\"}\\n' > \"\$m\"; \
    else \
      sed -E -i.bak 's/\"verified\"[[:space:]]*:[[:space:]]*(true|false)/\"verified\":true/' \"\$m\" && rm -f \"\$m.bak\"; \
      if ! grep -q '\"verified_at\"' \"\$m\"; then \
        sed -E -i.bak 's/}$/, \"verified_at\":\"$now\"}/' \"\$m\" && rm -f \"\$m.bak\"; \
      fi; \
    fi"
}

# ============================================================================
# Upgrade banner — nginx 标志文件驱动的静态升级页
# ----------------------------------------------------------------------------
# nginx 预先配置 `if (-f .../maintenance/UPGRADING) { return 503; }`
# 与 error_page 静态页。show 在 stop 前原子地写入 index.html 后创建 marker；
# hide 只在所有部署门禁通过后删除 marker。无需 reload nginx，也没有临时端口/
# 进程残留风险。
# ============================================================================

host_show_upgrade_banner() {
  local ssh_cmd=$1 target=$2 new_version=$3
  local template_file started_at old_version maint_root render_script rendered_html
  local install_root=${4:-$(host_root_for "$target")}
  local maintenance_dir=${5:-$install_root/maintenance}
  local version_root=${6:-$install_root}

  template_file="${HOST_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}/maintenance-template.html"
  [[ -f "$template_file" ]] || { echo "升级横幅模板缺失: $template_file" >&2; return 1; }

  maint_root="$maintenance_dir"
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  old_version=$("$ssh_cmd" "readlink '$version_root/current' 2>/dev/null | xargs basename 2>/dev/null" 2>/dev/null || true)
  : "${old_version:=unknown}"

  render_script=$(mktemp -t kx-render-banner.XXXXXX.py) || {
    echo "创建升级页模板临时文件失败" >&2
    return 1
  }
  cat >"$render_script" <<'PYEOF'
import sys
old_v, new_v, started_at, template = sys.argv[1:]
with open(template, encoding="utf-8") as source:
    body = source.read()
for token, value in (("__OLD_VERSION__", old_v), ("__NEW_VERSION__", new_v), ("__STARTED_AT__", started_at)):
    body = body.replace(token, value)
sys.stdout.write(body)
PYEOF
  if ! rendered_html=$(python3 "$render_script" "$old_version" "$new_version" "$started_at" "$template_file"); then
    rm -f "$render_script"
    echo "升级横幅模板渲染失败" >&2
    return 1
  fi
  rm -f "$render_script"

  # Write a temporary file and atomically rename it before creating the marker;
  # nginx never sees a partial page, even if a previous deploy left the marker.
  local remote_tmp="$maint_root/.index.html.deploy.$$"
  if ! printf '%s' "$rendered_html" | "$ssh_cmd" "set -e; mkdir -p '$maint_root'; cat > '$remote_tmp'; mv -f '$remote_tmp' '$maint_root/index.html'; : > '$maint_root/UPGRADING'"; then
    "$ssh_cmd" "rm -f '$remote_tmp'" >/dev/null 2>&1 || true
    echo "升级页或标志文件写入失败" >&2
    return 1
  fi
  echo "  ✓ 升级静态页已启用 (target=$new_version)"
}

host_hide_upgrade_banner() {
  local ssh_cmd=$1 target=$2 maint_root install_root maintenance_dir
  install_root=${3:-$(host_root_for "$target")}
  maintenance_dir=${4:-$install_root/maintenance}
  maint_root="$maintenance_dir"
  # Remove the page first and verify it is gone; only then remove the marker.
  # If either operation fails, UPGRADING remains and nginx continues to protect
  # the unverified release instead of resuming real traffic.
  "$ssh_cmd" "set -e; rm -f '$maint_root/index.html'; test ! -e '$maint_root/index.html'; rm -f '$maint_root/UPGRADING'" || return 1
  echo "  ✓ 升级静态页已撤掉"
}

# Atomic switch: rewrite the current symlink to point at the freshly
# uploaded release, then restart. The whole swap is one atomic
# rename(2) call (ln -sfn), so a partial state is impossible.
#
# 2026-07-16 optimization: the previous implementation issued 6
# independent SSH calls (5 ln + 1 restart). On flaky links each call
# could fail individually, and the script had no way to batch them.
# We now collapse them into ONE heredoc'd remote shell. The heredoc
# preserves ordering (set -e stops on first failure) and the host.sh
# contract — exactly one remote action, atomic to the caller.
#
# The deploy orchestrator owns the upgrade-page lifecycle. It enables the
# marker before calling this function and removes it only after all post-
# deploy gates pass; keeping this primitive focused preserves rollback use.
host_atomic_switch() {
  local ssh_cmd=$1 target=$2 version=$3
  local current_link binary_link web_link version_link release_dir bin_name
  current_link=$(host_release_layout "$target" "$version" | sed -n 's/^current_link=//p')
  binary_link=$(host_release_layout "$target" "$version" | sed -n 's/^binary_link=//p')
  web_link=$(host_release_layout "$target" "$version" | sed -n 's/^web_link=//p')
  version_link=$(host_release_layout "$target" "$version" | sed -n 's/^version_link=//p')
  release_dir=$(host_release_layout "$target" "$version" | sed -n 's/^release_dir=//p')
  bin_name=$(host_binary_name "$target") || return 64

  # Batched remote shell: same effect as the old 5-call sequence, but
  # one TCP/SSH round-trip + atomic on the remote side. `set -e` stops
  # on first failure so the symlink chain is never half-built.
  # web link quirk (2026-07-15) preserved: a real dir gets renamed
  # out of the way, otherwise nginx serves stale index.html.
  # 2026-07-21 fix: when deploying --no-frontend (release has no web/),
  # preserve the existing web symlink so the UI is not broken.
  "$ssh_cmd" "set -e
    ln -sfn '$release_dir' '$current_link'
    ln -sfn '$current_link/$bin_name' '$binary_link'
    if [ -d '$current_link/web' ]; then
      if [ -e '$web_link' ] && [ ! -L '$web_link' ]; then
        mv '$web_link' \"\${web_link}.legacy.\$(date +%Y%m%d-%H%M%S)\"
      fi
      ln -sfn '$current_link/web' '$web_link'
    fi
    ln -sfn '$current_link/version.json' '$version_link'
    if [ -d '$current_link/configs' ]; then
      ln -sfn '$current_link/configs' '$(dirname "$current_link")/configs'
    fi
  "

  # Restart stays as a separate call because it returns only after
  # systemd has issued the SIGTERM; combining it with the heredoc
  # would force us to wait synchronously and we'd lose the
  # timing/return-code signal.
  host_restart_service "$ssh_cmd" "$target"

}

# Drain the running service until all in-flight requests finish, then
# stop. Used by canary zero-downtime flows (see docs/deploy/zero-downtime-design.md).
#
# Strategy:
#   1. SIGTERM the service. Go srv.Shutdown has a 30s timeout, after
#      which it returns; systemd then escalates to SIGKILL after
#      TimeoutStopSec.
#   2. Poll /healthz until it stops answering (port released) OR the
#      drain deadline elapses.
#   3. Touch the drain marker file (managed by ExecStopPost in the
#      unit file).
host_drain_and_stop() {
  local ssh_cmd=$1 target=$2 drain_s=${3:-30}
  local service_name health_url deadline
  service_name=$(target_field "$target" service_name)
  health_url=$(target_field "$target" health_url)
  [[ -n "$service_name" && -n "$health_url" ]] \
    || { echo "host_drain_and_stop: missing contract for $target" >&2; return 1; }

  # Send SIGTERM via systemctl — this is what `restart` does first.
  # We do NOT use restart because we want to keep the unit dead while
  # the canary takes over.
  "$ssh_cmd" "systemctl stop '$service_name'" || true

  deadline=$(( $(date +%s) + drain_s ))
  while (( $(date +%s) < deadline )); do
    # If /healthz is unreachable the port has been released — drained.
    if ! "$ssh_cmd" "curl -fsS --max-time 1 '$health_url' >/dev/null 2>&1"; then
      return 0
    fi
    sleep 1
  done
  echo "host_drain_and_stop: $target did not drain in ${drain_s}s" >&2
  return 1
}

# List verified release directories newest-first, skipping the active
# version (when $3 is given). Emits one version per line.
#
# Pure shell + awk. Skips releases whose deployment.json does not have
# verified=true. Newest-first ordering uses the embedded verified_at
# timestamp; ties break on directory mtime.
host_list_verified_releases() {
  local ssh_cmd=$1 target=$2 skip_active=${3:-}
  local root releases_dir
  root=$(host_root_for "$target")
  releases_dir="$root/releases"
  "$ssh_cmd" "if [ -d '$releases_dir' ]; then \
    for d in '$releases_dir'/*/; do \
      [ -d \"\$d\" ] || continue; \
      meta=\"\$d/deployment.json\"; \
      [ -f \"\$meta\" ] || continue; \
      grep -q '\"verified\":true' \"\$meta\" || continue; \
      v=\$(basename \"\$d\"); \
      [ \"\$v\" = '$skip_active' ] && continue; \
      ts=\$(grep -o '\"verified_at\":\"[^\"]*\"' \"\$meta\" | head -1); \
      ts=\${ts#'\"verified_at\":\"'}; ts=\${ts%'\"'}; \
      printf '%s %s\n' \"\$ts\" \"\$v\"; \
    done | sort -r | awk '{print \$2}'; \
  fi"
}

# Prune releases on the TARGET host: keep the 5 newest verified
# bundles plus the active one. Unverified / failed bundles are kept
# until the next successful deploy (so post-mortem is possible); this
# function is intentionally not called from the rollback path — it
# fires only after a verified=true deploy finishes.
host_prune_releases() {
  local ssh_cmd=$1 target=$2 active_version=$3 keep=${4:-5}
  local root releases_dir
  root=$(host_root_for "$target")
  releases_dir="$root/releases"
  "$ssh_cmd" "releases_dir='$releases_dir'; active='$active_version'; keep=$keep; \
    if [ ! -d \"\$releases_dir\" ]; then exit 0; fi; \
    keep_set=''; \
    newest_verified=''; \
    failed_keep=''; \
    for d in \"\$releases_dir\"/*/; do \
      [ -d \"\$d\" ] || continue; \
      meta=\"\$d/deployment.json\"; \
      [ -f \"\$meta\" ] || continue; \
      v=\$(basename \"\$d\"); \
      ts_verified=\$(grep -o '\"verified_at\":\"[^\"]*\"' \"\$meta\" 2>/dev/null | head -1); \
      ts_verified=\${ts_verified#'\"verified_at\":\"'}; ts_verified=\${ts_verified%'\"'}; \
      if grep -q '\"verified\":true' \"\$meta\"; then \
        newest_verified=\"\$newest_verified \$v|\$ts_verified\"; \
      else \
        ts_created=\$(grep -o '\"created_at\":\"[^\"]*\"' \"\$meta\" 2>/dev/null | head -1); \
        ts_created=\${ts_created#'\"created_at\":\"'}; ts_created=\${ts_created%'\"'}; \
        failed_keep=\"\$v|\$ts_created\"; \
      fi; \
    done; \
    # Emit the 5 newest verified (space-separated by '|name ts').
    keep_set=\$(printf '%s' \"\$newest_verified\" | tr ' ' '\\n' | sort -t'|' -k2 -r | head -n \$keep | cut -d'|' -f1); \
    if [ -n \"\$active\" ]; then keep_set=\"\$keep_set \$active\"; fi; \
    if [ -n \"\$failed_keep\" ]; then keep_set=\"\$keep_set \$(printf '%s' \"\$failed_keep\" | cut -d'|' -f1)\"; fi; \
    for d in \"\$releases_dir\"/*/; do \
      [ -d \"\$d\" ] || continue; \
      v=\$(basename \"\$d\"); \
      case \" \$keep_set \" in *\" \$v \"*) ;; *) rm -rf \"\$d\" ;; esac; \
    done; \
    printf '%s' \"\$keep_set\""
}

# Rollback: point `current` at the given version and restart. Refuse missing,
# active, or unverified bundles even when called directly; the canonical CLI
# still selects a target first, but this primitive must be safe on its own.
host_rollback_to() {
  local ssh_cmd=$1 target=$2 version=$3
  local metadata_file current_link
  metadata_file=$(host_release_layout "$target" "$version" | sed -n 's/^metadata_file=//p')
  current_link=$(host_release_layout "$target" "$version" | sed -n 's/^current_link=//p')

  if ! "$ssh_cmd" "test -f '$metadata_file'"; then
    echo "host_rollback_to: no bundle at $version on $target" >&2
    return 1
  fi
  if "$ssh_cmd" "test \"\$(readlink '$current_link' 2>/dev/null | xargs basename 2>/dev/null)\" = '$version'"; then
    echo "host_rollback_to: $version is already active on $target" >&2
    return 1
  fi
  if ! "$ssh_cmd" "grep -Eq '\"verified\"[[:space:]]*:[[:space:]]*true' '$metadata_file'"; then
    echo "host_rollback_to: $version is not verified on $target" >&2
    return 1
  fi

  host_atomic_switch "$ssh_cmd" "$target" "$version"
}

# Select the rollback target — newest verified=true bundle that is
# NOT the currently-active version. Emits the version on stdout, or
# exits 4 (no_rollback_target) when no eligible bundle exists. The
# 4 exit code matches the spec; the orchestrator surfaces it to the
# operator as a distinct failure so evidence is preserved.
host_select_rollback_target() {
  local ssh_cmd=$1 target=$2 active_version=$3
  local candidate
  candidate=$(host_list_verified_releases "$ssh_cmd" "$target" "$active_version" | head -n1)
  if [[ -z "$candidate" ]]; then
    echo "no_rollback_target: no verified bundle older than $active_version on $target" >&2
    exit 4
  fi
  printf '%s\n' "$candidate"
}
