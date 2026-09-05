#!/usr/bin/env bash
# 回归测试：deploy-local.sh 的 .env.local 导入语义（dl_load_project_env）
#
# 背景（2026-09-05 事故）：deploy-local.sh 曾把 .env.local 的加载整体挂在
# “调用方没有提供 DATABASE_URL”这一条件上。任何导出了 DSN 的部署 shell 都会
# 跳过整个文件，LLM_GATEWAY_SECRET_KEY / ADMIN 凭据 / 凭据加密键随之丢失：
# 网关启动后无法签发管理端会话（登录一律 500 "token generation failed"），
# 且历史 token 全部验签失败，表现为“登录状态丢失”。2.5.0.x 系列本地部署
# 全部踩中该缺陷。
#
# 本测试锁定修复后的语义：
#   - 调用方非空值保持权威（CI/生产包装器可注入自己的 DSN）
#   - 调用方未设置或为空的键从 .env.local 补齐（空值视为未设置）
#   - 多行引号值（PEM 块）原样保留
#   - deploy-local.sh 保留空 secret 的 fail-fast 守卫

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LIB="$PROJECT_ROOT/scripts/deploy-local-lib.sh"
DEPLOY="$PROJECT_ROOT/scripts/deploy-local.sh"

TMPDIR_TEST="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_TEST"' EXIT

PASS=0
FAIL=0

report() {
    if [[ "$2" == "0" ]]; then
        echo "   ✅ $1"
        PASS=$((PASS + 1))
    else
        echo "   ❌ $1"
        FAIL=$((FAIL + 1))
    fi
}

cat > "$TMPDIR_TEST/env.local" <<'EOF'
export LLM_GATEWAY_DATABASE_URL="postgres://fileuser:filepass@127.0.0.1:5432/filedb"
export LLM_GATEWAY_SECRET_KEY="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
export LLM_GATEWAY_LICENSE_PUBLIC_KEY="-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA
multi-line-value-must-survive
-----END PUBLIC KEY-----"
EOF

# 在接近干净的环境中执行一段加载逻辑，模拟不同调用方 shell。
# 顺序镜像 deploy-local.sh：先 DSN 归一化（caller DSN 占住规范键），
# 再 dl_load_project_env 导入，最后再做一次归一化回填 DATABASE_URL 别名。
run_loader() {
    local caller_env="$1"
    env -i HOME="$HOME" PATH="$PATH" $caller_env /bin/bash -c '
        set -euo pipefail
        source "$1"
        if [[ -z "${LLM_GATEWAY_DATABASE_URL:-}" && -n "${DATABASE_URL:-}" ]]; then
            export LLM_GATEWAY_DATABASE_URL="$DATABASE_URL"
        fi
        if [[ -z "${DATABASE_URL:-}" && -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
            export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
        fi
        dl_load_project_env "$2"
        if [[ -z "${DATABASE_URL:-}" && -n "${LLM_GATEWAY_DATABASE_URL:-}" ]]; then
            export DATABASE_URL="$LLM_GATEWAY_DATABASE_URL"
        fi
        printf "dsn=%s|alias=%s|secret=%s" \
            "${LLM_GATEWAY_DATABASE_URL:-}" \
            "${DATABASE_URL:-}" \
            "${LLM_GATEWAY_SECRET_KEY:-}"
    ' _ "$LIB" "$TMPDIR_TEST/env.local"
}

SECRET="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

# ---------------------------------------------------------------------------
echo ""
echo "✅ 测试 1（事故主场景）: 调用方只导出了 DATABASE_URL，secret 仍必须从 .env.local 导入"
out="$(run_loader 'DATABASE_URL=postgres://caller/callerdb')"
if [[ "$out" == *"secret=$SECRET"* ]] && [[ "$out" == *"dsn=postgres://caller/callerdb"* ]]; then
    report "caller DSN 保留 + secret 补齐（token generation failed 根因）" 0
else
    report "caller DSN 保留 + secret 补齐（token generation failed 根因）—— got: $out" 1
fi

echo ""
echo "✅ 测试 2: 调用方完全干净时，文件中的键全部导入，且 DSN 兼容别名被回填"
out="$(run_loader '')"
if [[ "$out" == "dsn=postgres://fileuser:filepass@127.0.0.1:5432/filedb|alias=postgres://fileuser:filepass@127.0.0.1:5432/filedb|secret=$SECRET" ]]; then
    report "干净 shell 导入全部键 + DATABASE_URL 回填" 0
else
    report "干净 shell 导入全部键 + DATABASE_URL 回填 —— got: $out" 1
fi

echo ""
echo "✅ 测试 3: 调用方导出了空 secret（导出但为空 == 未设置），必须被文件值填充"
out="$(run_loader 'DATABASE_URL=postgres://caller/callerdb LLM_GATEWAY_SECRET_KEY=')"
if [[ "$out" == *"secret=$SECRET"* ]]; then
    report "空值调用方变量被填充" 0
else
    report "空值调用方变量被填充 —— got: $out" 1
fi

echo ""
echo "✅ 测试 4: 多行引号值（PEM 块）原样保留"
out="$(
    env -i HOME="$HOME" PATH="$PATH" /bin/bash -c '
        set -euo pipefail
        source "$1"
        dl_load_project_env "$2"
        printf %s "$LLM_GATEWAY_LICENSE_PUBLIC_KEY"
    ' _ "$LIB" "$TMPDIR_TEST/env.local"
)"
expected=$'-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA\nmulti-line-value-must-survive\n-----END PUBLIC KEY-----'
if [[ "$out" == "$expected" ]]; then
    report "多行 PEM 值完整无损" 0
else
    report "多行 PEM 值完整无损 —— got: $out" 1
fi

echo ""
echo "✅ 测试 5: deploy-local.sh 语法检查 + fail-fast 守卫存在"
if bash -n "$DEPLOY" && bash -n "$LIB"; then
    report "bash -n 语法检查" 0
else
    report "bash -n 语法检查" 1
fi
if grep -q 'LLM_GATEWAY_SECRET_KEY is empty' "$DEPLOY" \
    && grep -q 'dl_load_project_env "$PROJECT_ROOT/.env.local"' "$DEPLOY" \
    && grep -q 'refusing to start instance' "$DEPLOY"; then
    report "空 secret 拒绝部署 + rollback/start 守卫存在" 0
else
    report "空 secret 拒绝部署 + rollback/start 守卫存在" 1
fi

echo ""
echo "✅ 测试 6: bump-version.sh 能解析不带 v 前缀的 git 标签（导航显示 v0.0.0 的根因）"
# 仓库 2026-09 起使用不带 v 前缀的 semver 标签（2.5.x）；bump-version.sh 曾只
# 匹配 v 前缀模式，匹配为空后回落 v0.0.0 并随每次 bump 自我延续。镜像脚本的
# 两段式解析取最新标签，断言 dry-run 输出的 target 版本以其开头。
latest_tag="$(git -C "$PROJECT_ROOT" tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n 1)"
if [[ -z "$latest_tag" ]]; then
    latest_tag="$(git -C "$PROJECT_ROOT" tag --list '[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n 1)"
fi
target_tag="$(bash "$PROJECT_ROOT/scripts/bump-version.sh" --dry-run 2>/dev/null \
    | sed -n 's/.*target:.*version=\([0-9v][0-9.]*\)-.*/\1/p' | head -n 1)"
if [[ -n "$latest_tag" && "$target_tag" == "${latest_tag#v}" ]]; then
    report "bump 标签解析 = 最新 semver 标签 [$target_tag]" 0
else
    report "bump 标签解析 = 最新 semver 标签 [latest=$latest_tag target=$target_tag]" 1
fi
if grep -q "git tag --list '\[0-9\]\*\.\[0-9\]\*" "$PROJECT_ROOT/scripts/bump-version.sh"; then
    report "裸 semver 回退模式存在" 0
else
    report "裸 semver 回退模式存在" 1
fi

# ---------------------------------------------------------------------------
echo ""
echo "========================================="
if (( FAIL == 0 )); then
    echo "全部通过（$PASS 项）"
else
    echo "失败 $FAIL 项 / 通过 $PASS 项"
    exit 1
fi
