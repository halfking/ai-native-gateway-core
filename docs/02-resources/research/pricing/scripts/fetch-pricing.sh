#!/usr/bin/env bash
# fetch-pricing.sh — 一键抓取各厂商最新价目 (agent-reach / r.jina.ai 路由)
# 输出: docs/02-resources/research/pricing/raw/{vendor}.md
#
# ── 三条实测出来的硬约束（2026-10-06），改这个脚本前先读 ──
#
# 1. **输出路径原本是错的，而且错得很隐蔽。**
#    `SCRIPT_DIR/../..` 从 `…/pricing/scripts` 落到 `…/02-resources/research`，
#    不是仓根；再拼 `services/llm-gateway-go/docs/pricing/raw` 得到一条
#    **不存在**的路径。`mkdir -p` 会把它悄悄建出来，于是脚本「成功」了，
#    而真正的 `raw/` 一个字节没变 —— 仓里那些快照**不可能**是它抓的
#    （实测：`anthropic` 快照的 `URL Source` 是 `…/about-claude/pricing`，
#    而本脚本抓的是 `…/models/overview`）。**三个路径声明彼此不一致，
#    而写进 SSOT 草稿 `source_url` 的是 `cmd/tools/propose-baseline-prices`
#    的 `vendorPage` 那一个** —— 也就是错误出处会被冻结进权威面。
#
# 2. **`--max-time 30` 太短，而 `-s` 让超时不响。**
#    实拍 `ai.google.dev/gemini-api/docs/pricing` 要 **52.679 秒**。
#    超时后 curl 写出**空文件**、退出码为 0，脚本照样打印
#    `→ …（0 bytes）` 并继续 —— 下一次重跑就会拿空快照盖掉好快照。
#    ⇒ 超时必须**响**，且落盘前要断言内容非空。
#
# 3. **`openai` 抓的是 models 页，不是 pricing 页。**
#    models 页 47,490 字节、**0 张表格、1 行含 `$`**；pricing 页 61,525
#    字节、**92 张表格、54 行含 `$`**。抓 models 页 ⇒ openai 永远 0 候选。
#
# 判据：`cmd/tools/propose-baseline-prices/url_consistency_test.go`
# （快照 `URL Source` 必须等于 `vendorPage` 的 URL；`vendorPage` 里有的键，
# 抓取清单里也必须有）。改 URL 时两个地方一起改。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# ↑ 仓根是 scripts/ 上溯**五**级：pricing → research → 02-resources → docs → <root>
#
# ★ 2026-10-06 二次修正：这里曾是**四级**，落在 `docs/` 而不是仓根，于是
#   `$OUT` 变成 `docs/docs/02-resources/...`，`mkdir -p` 把一条**假树**建出来，
#   脚本照样打印 `→ …（N bytes, M 张表格）`，而真正的 raw/ 一个字节没变。
#   讽刺的是第 1 条约束写的就是这个坑（还举了 `services/llm-gateway-go/docs`
#   那个旧版本）—— 同一个 off-by-one，隔了一轮又长回来。
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../../.." && pwd)"
OUT="$REPO_ROOT/docs/02-resources/research/pricing/raw"

# ★ 不再 `mkdir -p`。快照目录是**随仓存在**的产物目录，现场创建它只有一个
#   后果：路径算错时不报错，而是造出一棵没人会去看的新树，而抓取结果显示
#   「成功」。⇒ 目录不存在就是**致命错误**，这里必须响。
if [ ! -d "$OUT" ]; then
  echo "❌ 快照目录不存在：$OUT" >&2
  echo "   路径算错时 mkdir -p 会造出一棵假树并让本脚本「成功」，而真正的 raw/" >&2
  echo "   一个字节没变 —— 所以这里刻意不创建。仓根算对了吗？" >&2
  echo "   SCRIPT_DIR=$SCRIPT_DIR" >&2
  echo "   REPO_ROOT=$REPO_ROOT" >&2
  exit 1
fi
# 目录在，但落在仓外 —— 同样致命（写到别处去了）。
case "$OUT" in
  "$REPO_ROOT"/docs/02-resources/research/pricing/raw) ;;
  *) echo "❌ $OUT 不等于预期的仓内路径，路径解析有问题" >&2; exit 1 ;;
esac

# 低于这个字节数一律视为抓取失败（实测最小可用快照 moonshot 3,292 字节）。
MIN_BYTES=1000
# 60s 起：google pricing 实测 52.7s，30s 会静默截断。
MAX_TIME=90

failed=()

fetch() {
  local vendor="$1" url="$2"
  local dest="$OUT/${vendor}.md"
  local tmp="$dest.partial"
  printf '📥 Fetching %-22s %s\n' "$vendor" "$url"

  local code
  code=$(curl -sL --max-time "$MAX_TIME" "https://r.jina.ai/$url" \
    -H "User-Agent: Mozilla/5.0" \
    -o "$tmp" -w '%{http_code}' || echo "000")

  # 三件事都算失败：HTTP 非 200、curl 没写成文件、文件小得不可能是定价页。
  if [ "$code" != "200" ] || [ ! -s "$tmp" ] || [ "$(wc -c < "$tmp")" -lt "$MIN_BYTES" ]; then
    printf '  ❌ %s: http=%s bytes=%s — 抓取失败，**不覆盖**已有快照\n' \
      "$vendor" "$code" "$( [ -f "$tmp" ] && wc -c < "$tmp" || echo 0 )"
    rm -f "$tmp"
    failed+=("$vendor")
    return 0
  fi

  # 快照头里的 URL Source 必须与请求的 URL 一致。Jina reader 偶尔会重定向到
  # 别的页（拿到 models 页而不是 pricing 页就是这样发生的），而那**不会**报错 ——
  # 只会安静地产出一份没有价钱的快照。
  local got
  got=$(grep -m1 '^URL Source:' "$tmp" | sed 's/^URL Source: *//' || true)
  if [ -n "$got" ] && [ "${got%/}" != "${url%/}" ]; then
    printf '  ❌ %s: 落地页是 %s，与请求的 %s 不符 — 抓取失败，**不覆盖**已有快照\n' \
      "$vendor" "$got" "$url"
    rm -f "$tmp"
    failed+=("$vendor")
    return 0
  fi

  mv "$tmp" "$dest"
  printf '  → %s (%s bytes, %s 张表格, %s 行含 $)\n' \
    "$dest" "$(wc -c < "$dest")" \
    "$(grep -c '^\s*|.*|\s*$' "$dest" || true)" \
    "$(grep -c '\$' "$dest" || true)"
}

# ── 原厂定价页 ──────────────────────────────────────────────────────────
# 与 cmd/tools/propose-baseline-prices 的 vendorPage 一一对应（判据会核）。
fetch anthropic     "https://docs.anthropic.com/en/docs/about-claude/pricing"
fetch openai        "https://platform.openai.com/docs/pricing"
fetch google-gemini "https://ai.google.dev/gemini-api/docs/pricing"
fetch deepseek      "https://api-docs.deepseek.com/quick_start/pricing"
fetch xai           "https://docs.x.ai/docs/pricing"
fetch zhipu         "https://open.bigmodel.cn/pricing"
fetch MiniMax-paygo "https://platform.minimax.io/docs/guides/pricing-paygo"
fetch mistral       "https://docs.mistral.ai/getting-started/models/models_overview"
fetch doubao        "https://www.volcengine.com/docs/82379/1544106"
# moonshot 在 vendorPage 里**故意**没有：目录里 21 条 moonshot 模型，但它的
# 价目表形状未验证过。抓下来供人工看，不进提案。
fetch moonshot      "https://platform.moonshot.ai/docs/pricing/chat"

# ★ 抓取清单里的名字必须与 vendorPage 的**键**逐字一致（判据会核）。
#   原先这里是 `fetch minimax`，产出 minimax.md，而提案工具读的是
#   MiniMax-paygo.md —— 于是重跑脚本**永远不会**更新工具读的那一份，
#   而脚本对自己抓到的文件是满意的，**不会报这件事**。

# ── 聚合站（交叉验证用，提案工具会 skip）────────────────────────────────
fetch openrouter    "https://openrouter.ai/api/v1/models"

echo ""
if [ "${#failed[@]}" -gt 0 ]; then
  printf '❌ %d 个页面抓取失败：%s\n' "${#failed[@]}" "${failed[*]}"
  printf '   已有快照**未被覆盖**。重跑本脚本只会重试这些页面。\n'
  exit 1
fi
printf '✅ Done. %s has %s files.\n' "$OUT" "$(ls "$OUT" | wc -l | tr -d ' ')"
ls -la "$OUT"
