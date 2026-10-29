package ursm_test

// gocache_script_family_guard_test.go —— r1048 轮（2026-10-29）：
// 【B 级脚本族】（★ 审计 / 部署 / 压测 / 一次性验证那一族）的【专用构建缓存】门。
//
// # 这个文件补的是 r1047 轮**自己写下的缺口**
//
// ★★★ **r1047 轮的门只覆盖了两族**：★★★
// ★★★ ① **Go 文件**里的子进程编译点（★ `exec.Command("go", …)`）⇒ ⇒ 第一面；
// ★★★ ② **A 级脚本**（★ `scripts/mutate-*` / `scripts/.mutate-*` / `scripts/audit/mutate_*`）
// ★★★ ⇒ ⇒ ★★ **第二面**。
// ★★★ ⇒ ⇒ ★★★ 而 r1047 轮的审计档 §六 **诚实地**把【B 级脚本族】写成
// ★★★ 【**明确的漏判面**】⇒ ⇒ ★★ 因为「**假装覆盖了**」比「**承认没覆盖**」更危险。
// ★★★ ⇒ ⇒ ★★ ⇒ ★★ **本文件就是那个缺口的兑现**。
//
// # 为什么要补（★ 实测，★ 2026-10-29，本机）
//
// ★★★★ **普查读数**：★★★★ 仓内（★ 排除 `docs/` / `web/` / `web-mobile/`）
// ★★★★ 有 **165 个** `.sh` / `.py` 文件调**编译型** go 子命令 ⇒ ⇒ ★★ 其中
// ★★★★ **A 级 68** 个（★ r1047 第二面已盖）、★ ★★ **B 级 97** 个
// ★★★★ （★ shell 94 + python 3）⇒ ⇒ ★★ ⇒ **B 级轮前覆盖 0** ⇒ ⇒ ★★ ⇒ 本门就是那一族。
//
// ★★★★ **为什么是「全隔离」而不是「按量分层豁免」**：★★★★ 实测（★ 两个**全新空缓存**，
// ★★★★ 2026-10-29 本机）：★★★ **单包** `go test ./admin/` ⇒ ⇒ **846 MB**；
// ★★★★ **整模块 + tag** `go build -tags=integration ./...` ⇒ ⇒ **997 MB**
// ★★★★ ⇒ ⇒ ★★ ⇒ **单包是整模块的 85%** ⇒ ⇒ ★★ ⇒ **体积由【传递依赖闭包】决定、
// ★★★★ ⇒ ⇒ 不由【包个数】决定** ⇒ ⇒ ★★ ⇒ ⇒ ★★ **「单包小、可以放过」是错的**
// ★★★★ ⇒ ⇒ ★★ ⇒ ⇒ **「按编译范围分层」在实测面前不成立** ⇒ ⇒ ★★ ⇒ ⇒ **全隔离**。
//
// ★★★★ **容器腿 vs 宿主腿（★ ★★ 普查里最值钱的一处发现）**：★★★
// ★★★★ `scripts/deploy-local.sh` / `scripts/deploy-252-gateway.sh` /
// ★★★★ `scripts/deploy-seamless.sh` 三家**都有两条编译腿**：★★★
// ★★★★ ① **宿主腿**：★ `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build …`
// ★★★★ ⇒ ⇒ ★★ **没有任何 GOCACHE** ⇒ ⇒ ★★ ⇒ **写进用户的全局缓存**；
// ★★★★ ② **容器腿**：★ `docker run … -e GOCACHE=/tmp/go-build-cache …`
// ★★★★ ⇒ ⇒ ★★ 容器**自带**缓存且 `--rm` ⇒ ⇒ ★★ ⇒ **本来就是隔离的**。
// ★★★★ ⇒ ⇒ ★★ ⇒ **r1047 轮普查读数「容器腿已隔离」是对的**，★ 但它**只说了
// ★★★★ 宿主腿没被提** ⇒ ⇒ ★★ ⇒ ★★ **同一份文件里，一条腿隔离、另一条腿泄漏**
// ★★★★ ⇒ ⇒ ★★ ⇒ ⇒ **这正是本门要抓的东西**。
//
// # 本门的【判据】（★ 拆成 5 个**独立**的 `Test` 函数 ⇒ ⇒ ★★ ⇒ 好让变异台**精准命中某一条**，★ 而不是一个红把其余都带红）
//
// ★ **判据①（覆盖面 + 证据形态 + 条件式不算隔离）**：★ 仓内每一个**在作用域内**、
// ★ 且含【**编译型** go 调用】的
// ★ `.sh` / `.py` 文件 ⇒ ⇒ 它必须把子进程的 `GOCACHE` 指向【**专用目录**】。
//
// ★ ★★ **①b 证据的形态**：★ 「指向专用目录」只认**两种形态**：★★★
// ★   ① **委托共享入口**：★ `gocache_use` / `gocache.sh` / `gocache.env(` /
// ★      `gocache.dir_for(` / `gocache.apply_to_environ(`；
// ★   ② **显式路径**：★ `.build-local/.gocache-…`（★ **注意是 `.gocache-`**）。
// ★ ★★ **为什么路径形态要写全 `.build-local/.gocache-` 而不是 `.build-local/`**：★★
// ★ ★★ ★★ `.build-local/` 在本仓**同时**用于【构建产物】（★ `gateway.build`）
// ★ ★★ ⇒ ⇒ ★★ ⇒ **只认 `.build-local/` 会把「产物落在 .build-local」误判成
// ★ ★★ ⇒ ⇒ 「缓存落在 .build-local」** ⇒ ⇒ ★★ ⇒ **假绿**（★ 实测 5 个文件命中
// ★ ★★ ⇒ ⇒ 这个坑：★ `deploy-local.sh` / `deploy-252-gateway.sh` /
// ★ ★★ ⇒ ⇒ `deploy-seamless.sh` / `local-deploy-test.sh` / `local-v4-build.sh`）。
//
// ★ ★★ **①c 条件式不算隔离**：★ `GOCACHE="${GOCACHE:-…}"` /
// ★ `setdefault("GOCACHE", …)` 是**条件式** ⇒ ⇒ 用户环境里已经设了 `GOCACHE`
// ★ ⇒ ⇒ ★★ **悄悄回落用全局缓存** ⇒ ⇒ ★★ ⇒ **门绿着，而隔离根本没生效**
// ★ ⇒ ⇒ ★★ ⇒ ★★ 所以**只有条件式证据**的文件**照样判红**（★ 与 r1047 的 M04 臂同款）。
//
// ★ **判据②（`env -i` 会抹掉顶部的 export）**：★ ★★★ 这是本轮【逐个读懂脚本】
// ★★★ 读出来的一处：★★★ `scripts/audit/measure-factsource-cost-matrix.sh` 用
// ★★★ `env -i "${BASE_ENV[@]}" … go test …` ⇒ ⇒ ★★★ **`env -i` 会清空环境**
// ★★★ ⇒ ⇒ ★★★ ⇒ 顶部 `export GOCACHE` **被抹掉** ⇒ ⇒ ★★★ ⇒ **隔离失效**
// ★★★ ⇒ ⇒ ★★★ ⇒ 而**门是绿的**（★ 门只看见「文件里设了 GOCACHE」）⇒ ⇒ ★★★
// ★★★ ⇒ ⇒ ★★★ ⇒ **这与判据①c 是同一族**：★「设了」≠「用上了」。
// ★★★ ⇒ ⇒ ★★★ ⇒ 所以：用 `env -i` 的脚本 ⇒ ⇒ **必须**有一个【非 export 形态】
// ★★★ ⇒ ⇒ ★★★ ⇒ 的 GOCACHE 赋值（★ 也就是**显式列进那个 env 数组**）。
//
// ★ **判据④（GOMODCACHE 不许进 `.build-local/`）**：★ ★★★ 模块缓存是
// ★★★ **内容寻址且可复用**的 ⇒ ⇒ ★★★ 它**不是垃圾** ⇒ ⇒ ★★★ **不该被隔离**；
// ★★★ ⇒ ⇒ ★★★ 而且隔离它 ⇒ ⇒ ★★★ `git clean -xfd` 会把它删掉 ⇒ ⇒ ★★★
// ★★★ ⇒ ⇒ ★★★ ⇒ 每次都要**重下整个模块图** ⇒ ⇒ ★★★ ⇒ **比不隔离更糟**。
// ★★★ ⇒ ⇒ ★★★ ⇒ 所以本门反过来管：★ **不许**把它指向 `.build-local/` 下。
//
// ★ **判据⑤（排除清单必须逐项核实）**：★ 排除清单登记簿里每一条排除路径
// ★ ⇒ ⇒ 它的【编译型脚本文件数】必须与登记值**逐字相等** ⇒ ⇒ ★★
// ★ ★★ **为什么必须逐字相等**：★★ ★★ 排除是**政策** ⇒ ⇒ ★★ 政策会漂
// ★ ★★ ⇒ ⇒ ★★ ⇒ **「新加一个 go 调用却没人发现」是排除清单最典型的失效形态**
// ★ ★★ ⇒ ⇒ ★★ ⇒ ★★ 所以任何新增都会让门红 ⇒ ⇒ ★★ ⇒ **逼着人重新审一遍**。
// ★ ★★ ⇒ ⇒ ★★ ⇒ 顺带堵住「登记簿里留着已经不存在的路径」（★ 过期登记 ⇒ ★
// ★ ★★ ⇒ ⇒ ★★ 静默地不再覆盖真实存在的东西）。
//
// ★ **（搜索面完整性自检，★ 并在判据① 那个 Test 里）**：★ 一个编译点都没扫到 ⇒ ⇒ **门是瞎的**
// ★ ⇒ ⇒ 门绿了也不能当「已覆盖」的证据（★ 仓内 §419 的通式：★ 「0 命中」有两种
// ★ ⇒ ⇒ 读法 ⇒ ★★ 「扫到了没发现」vs「根本没扫到那一段」）。
//
// # 本门的【覆盖边界】（★ 诚实登记，★ 别把它当全量尺）
//
// ★ ① **CI（`.github/workflows/*.yml`）不在作用域内** ⇒ ⇒ ★★ 理由：★ 那边的
// ★    `setup-go` 已经带 `cache: true`（★ GitHub 官方动作 ⇒ ⇒ 它**自己**管缓存
// ★    ⇒ ⇒ 有自己的生命周期）⇒ ⇒ ★★ 且 CI runner 的缓存**随 job 消亡**
// ★    ⇒ ⇒ ★★ ⇒ **不会污染任何人的全局缓存**。
// ★ ② **`docs/` / `web/` / `web-mobile/` 不在作用域内** ⇒ ⇒ ★★ 理由与**实测计数**
// ★    登记在 `gocache_script_family_exclusions.json` ⇒ ⇒ ★★ ⇒ **由判据⑤ 盯着**。
// ★ ③ **只认 `.sh` / `.py`** ⇒ ⇒ ★★ Makefile / 其它语言的脚本不在本门视野内。
// ★ ④ **按【子命令】分类**（★ 与 r1047 第一面**故意相反**）：★ 只对【**编译型**】（★ `build` / `test` / `vet` / `run`）要求隔离；
// ★    `go list` / `go env` / `go mod` **不写构建缓存** ⇒ ⇒ ★★ ⇒ **不要求**
// ★    ⇒ ⇒ ★★ ⇒ ★★ （★ 若一并要求 ⇒ ⇒ 就要对「只读元数据」的脚本也装隔离 ⇒ ⇒
// ★    ⇒ ⇒ ★★ ⇒ ⇒ **误伤面变大、而收益为零** ⇒ ⇒ ★★ ⇒ ⇒ **宁可少要求，也不要误伤**）
// ★    ⇒ ⇒ ★ **宁可多要求一点**，★ 也不要「按子命令分类」带来的漏判。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ── ★★ 作用域与登记簿 ──

// gocacheScriptExclusionRegistryRel 是【排除清单登记簿】的仓内相对路径。
const gocacheScriptExclusionRegistryRel = "scripts/ursmcheck/gocache_script_family_exclusions.json"

// gocacheScriptInfraSkipDirs 是【扫描时直接跳过】的目录。
//
// ★ ★★ **为什么它们不在登记簿里**：★★ 这一层是**基础设施**（★ `.git` 是对象库、
// ★★ `node_modules` 是前端依赖、★★ `vendor` 是 vendored 依赖、★★ `.build-local`
// ★★ 是**产物**目录 ⇒ ⇒ 它里面的东西**不是源码**）⇒ ⇒ ★★ **不构成「政策」** ⇒ ⇒
// ★★ ⇒ ⇒ 登记簿只登记**政策性排除**（★ `docs/` / `web/` / `web-mobile/`）。
var gocacheScriptInfraSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".build-local": true,
	"maestro":      true,
}

// gocacheScriptExclusionRegistry 是【排除清单登记簿】的结构。
type gocacheScriptExclusionRegistry struct {
	Note            string                        `json:"note"`
	MinCompileFiles int                           `json:"minCompileFiles"`
	ExcludedPaths   []gocacheScriptExclusionEntry `json:"excludedPaths"`
}

// gocacheScriptExclusionEntry 是登记簿里的一条排除记录。
type gocacheScriptExclusionEntry struct {
	// Path 是【相对仓根】的排除路径前缀。
	Path string `json:"path"`
	// Reason 是【为什么排除】——★ 不许为空（★ 空理由 ⇒ ⇒ 登记簿自己失效）。
	Reason string `json:"reason"`
	// CompileFiles 是【该路径下含编译型 go 调用的脚本文件数】。
	//
	// ★ ★★ **为什么必须逐字核实**：★★ 新加一个 go 调用却没人发现
	// ★★ ⇒ ⇒ ★★ ⇒ 是排除清单最典型的失效形态 ⇒ ⇒ ★★ ⇒ 所以门会拿实测值对拍。
	CompileFiles int `json:"compileFiles"`
}

// gocacheLoadExclusionRegistry 读登记簿。
func gocacheLoadExclusionRegistry(t *testing.T, root string) gocacheScriptExclusionRegistry {
	t.Helper()
	path := filepath.Join(root, gocacheScriptExclusionRegistryRel)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读排除清单登记簿失败（%s）：%v", gocacheScriptExclusionRegistryRel, err)
	}
	var reg gocacheScriptExclusionRegistry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("解析排除清单登记簿失败（%s）：%v", gocacheScriptExclusionRegistryRel, err)
	}
	if len(reg.ExcludedPaths) == 0 {
		t.Fatalf("排除清单登记簿是空的（%s）⇒ ⇒ 本门**没有任何排除**⇒ ⇒ "+
			"要么登记簿被清空了，要么扫描口径变了 ⇒ ⇒ 重新审一遍", gocacheScriptExclusionRegistryRel)
	}
	return reg
}

// gocacheExcludedPrefixes 返回登记簿里的排除前缀集合。
func gocacheExcludedPrefixes(reg gocacheScriptExclusionRegistry) map[string]bool {
	out := map[string]bool{}
	for _, e := range reg.ExcludedPaths {
		out[e.Path] = true
	}
	return out
}

// ── ★★ 形态判据的正则 ★─

// gocacheScriptCompileRE 认【编译型】go 子命令（★ shell 形态）。
var gocacheScriptCompileRE = regexp.MustCompile(`\bgo\s+(build|test|vet|run)\b`)

// gocacheScriptCompileListRE 认【编译型】go 子命令（★ Python 列表形态）。
var gocacheScriptCompileListRE = regexp.MustCompile(`["']go["']\s*,\s*["'](build|test|vet|run)["']`)

// gocacheScriptDedicatedRE 认【指向专用缓存】的形态。
//
// ★ ★★ **注意路径形态写的是 `.build-local/.gocache-`**（★ 不是 `.build-local/`）
// ★ ★★ ⇒ ⇒ 理由见文件头判据①b。
var gocacheScriptDedicatedRE = regexp.MustCompile(
	`\.build-local/\.gocache-|gocache\.sh|gocache_use|gocache\.env\(|gocache\.dir_for\(|gocache\.apply_to_environ\(`)

// gocacheScriptSetdefaultRE 认【条件式】的 GOCACHE 设定。
//
// ★ ★★ **为什么单列一条**：★★ 条件式 ⇒ ⇒ 用户环境里已设 ⇒ ⇒ 悄悄回落
// ★ ★★ ⇒ ⇒ ⇒ **门绿着，而隔离根本没生效** ⇒ ⇒ ★★ ⇒ 所以它**不算隔离证据**。
var gocacheScriptSetdefaultRE = regexp.MustCompile(
	`\$\{GOCACHE:-|setdefault\(\s*["']GOCACHE["']|GOCACHE["']\]\s*=\s*os\.environ\.get\(`)

// gocacheScriptGocacheAssignRE 认【任何形态】的 GOCACHE 赋值。
var gocacheScriptGocacheAssignRE = regexp.MustCompile(`\bGOCACHE\s*=`)

// gocacheScriptEnvDashI_RE 认 `env -i`（★ 清空环境）。
var gocacheScriptEnvDashI_RE = regexp.MustCompile(`\benv\s+-i\b`)

// gocacheScriptPassthroughRE 认【把已导出的 GOCACHE 原样带进子进程 env】的写法。
//
// ★ ★★ **为什么它也算隔离证据**：★★ `env -i` 会清空环境 ⇒ ⇒ ★★ 顶部那个
// ★★ `export GOCACHE` 被抹掉 ⇒ ⇒ ★★ ⇒ 但如果 env 数组里写的是
// ★★ `GOCACHE="$GOCACHE"` ⇒ ⇒ ★★ ⇒ **带进去的就是那个专用目录** ⇒ ⇒ ★★ ⇒ 隔离成立。
// ★★ ⇒ ⇒ ★★ ★★ 但这**有前提**：★★ 那个被引用的 `$GOCACHE` 本身**必须**是专用的
// ★★ ⇒ ⇒ ★★ ⇒ 所以还要叠一条「本文件别处有【无条件】的专用指向」（★ 判据② 里查）。
var gocacheScriptPassthroughRE = regexp.MustCompile(`\bGOCACHE\s*=\s*"?\$\{?GOCACHE\}?"?`)

// gocacheScriptUserGlobalValueRE 认【GOCACHE 的取值】指向仓外用户目录。
//
// ★ ★★ **为什么必须按「取值」而不是按「整行」判**：★★ ★★ 整行判会**误伤四类**：
// ★ ★★ ① 同一行上的**别的**变量（★ 实测：`GOCACHE="$GOCACHE" GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"`
// ★ ★★ ⇒ ⇒ 整行里有 `$HOME` ⇒ ⇒ 但那是 **GOMODCACHE** 的值 ⇒ ⇒ **不是 GOCACHE 的**）；
// ★ ★★ ② **容器腿**的 `-e GOCACHE=/tmp/go-build-cache`（★ 那是**容器自己**的缓存，
// ★ ★★ ⇒ ⇒ 而且容器是 `--rm` ⇒ ⇒ ⇒ 本来就是隔离的）；
// ★ ★★ ③ **字符串常量**里出现的 `GOCACHE=`（★ 实测：★ r1047 变异台的 M04 臂载荷
// ★ ★★ ⇒ ⇒ `M04_REPL = 'export GOCACHE="$HOME/.cache/go-build" …'` ⇒ ⇒ ★★ 那是**数据**
// ★ ★★ ⇒ ⇒ ⇒ 不是代码 ⇒ ⇒ ★★ ⇒ ⇒ **必须先确认它不在引号里**）；
// ★ ★★ ④ 注释。
var gocacheScriptUserGlobalValueRE = regexp.MustCompile(`\$\{?HOME\b|~/|expanduser|go-build|sdk/gocache|Library/Caches`)

// gocacheGocacheAssign 是一个【变量赋值点】。
type gocacheGocacheAssign struct {
	// Line 是 1 起的行号。
	Line int
	// Rest 是【`=` 之后到「下一个赋值」或行尾】的原文（★ 供判据看「取值」）。
	Rest string
	// Text 是那一行的原文（★ 供报错时展示）。
	Text string
}

// gocacheScriptLine 是一行源码 + 它的【代码掩码】。
//
// ★ ★★ **掩码是这一整块判据的地基**：★★ 掩码的每一位标出「这个字节是【代码】
// ★ ★★ 还是【数据】（★ 字符串字面量 / 注释）」⇒ ⇒ ★★ ⇒ 「这个 `GOCACHE=` 是不是
// ★ ★★ 一次**真正的赋值**」⇒ ⇒ ★★ ⇒ 就成了一个**位置问题** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ 而不是「这一行里有没有出现过 `GOCACHE=`」这种**文本问题**。
type gocacheScriptLine struct {
	Text string
	// IsCode[i] 标出 Text[i] 是否属于【代码】。
	IsCode []bool
}

// gocacheScriptMaskPython 给 Python 源码打掩码。
//
// ★ ★★ **为什么 Python 与 shell 要分开**：★★ Python 的单引号/双引号字符串
// ★ ★★ **不能跨行**（★ 除非行尾续行）⇒ ⇒ ★★ ⇒ 而 **三引号字符串可以跨行**
// ★ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ **模块级 docstring 就是跨行的** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ **不处理跨行 ⇒ 整段 docstring 会被当成代码** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ ⇒ **实测踩到过**：★ 本轮自己的变异台 docstring 里写着
// ★ ★★ ⇒ `export GOCACHE="$HOME/.cache/go-build"` ⇒ ⇒ ★★ ⇒ ⇒ **被门当成了真赋值**。
func gocacheScriptMaskPython(code []string) []gocacheScriptLine {
	out := make([]gocacheScriptLine, 0, len(code))
	// inTriple 是当前跨行的三引号定界符（★ "" 或 ''）；★ 空串 = 不在跨行字符串里。
	inTriple := ""
	for _, line := range code {
		mask := make([]bool, len(line))
		i := 0
		for i < len(line) {
			if inTriple != "" {
				// ★ 在跨行字符串里 ⇒ ⇒ 直到闭合定界符都是数据。
				if strings.HasPrefix(line[i:], inTriple) {
					for j := 0; j < len(inTriple) && i+j < len(line); j++ {
						mask[i+j] = false
					}
					i += len(inTriple)
					inTriple = ""
					continue
				}
				mask[i] = false
				i++
				continue
			}
			c := line[i]
			switch {
			case c == '#':
				// ★ 注释到行尾（★ Python 没有行续注释）。
				for ; i < len(line); i++ {
					mask[i] = false
				}
			case strings.HasPrefix(line[i:], `"""`):
				inTriple = `"""`
				for j := 0; j < 3 && i+j < len(line); j++ {
					mask[i+j] = false
				}
				i += 3
			case strings.HasPrefix(line[i:], `'''`):
				inTriple = `''`
				for j := 0; j < 3 && i+j < len(line); j++ {
					mask[i+j] = false
				}
				i += 3
			case c == '\'' || c == '"':
				// ★ 单行字符串（★ Python 里不能跨行，★ 除非反斜杠续行）。
				mask[i] = false
				i++
				for i < len(line) {
					if line[i] == '\\' && i+1 < len(line) {
						mask[i] = false
						mask[i+1] = false
						i += 2
						continue
					}
					mask[i] = false
					if line[i] == c {
						i++
						break
					}
					i++
				}
			default:
				mask[i] = true
				i++
			}
		}
		out = append(out, gocacheScriptLine{Text: line, IsCode: mask})
	}
	return out
}

// gocacheScriptMaskShell 给 shell 源码打掩码。
//
// ★ ★★ **shell 的引号可以跨行** ⇒ ⇒ ★★ ⇒ ⇒ 状态**跨行保留**（★ 这与 Python 不同）。
// ★ ★★ `#` 只有在【词首】才是注释（★ 否则 `a#b` 是一个词）⇒ ⇒ ★★ ⇒ ★★ ⇒
// ★ ★★ 「同一行上的别的变量」这条假阳性也顺带被这里挡住（★ 因为取值是按
// ★ ★★ 「下一个赋值」切的，★ 不是按行尾切的）。
func gocacheScriptMaskShell(code []string) []gocacheScriptLine {
	out := make([]gocacheScriptLine, 0, len(code))
	inSingle, inDouble := false, false
	for _, line := range code {
		mask := make([]bool, len(line))
		i := 0
		for i < len(line) {
			if inSingle || inDouble {
				mask[i] = false
				if inDouble && line[i] == '\\' && i+1 < len(line) {
					mask[i+1] = false
					i += 2
					continue
				}
				if inSingle && line[i] == '\'' {
					inSingle = false
				}
				if inDouble && line[i] == '"' {
					inDouble = false
				}
				i++
				continue
			}
			c := line[i]
			switch {
			case c == '#':
				// ★ 词首的 `#` 才是注释（★ 前面是空白 / 行首 / `;` `&` `|`）。
				prevOK := i == 0 || line[i-1] == ' ' || line[i-1] == '\t' ||
					line[i-1] == ';' || line[i-1] == '&' || line[i-1] == '|'
				if prevOK {
					for ; i < len(line); i++ {
						mask[i] = false
					}
				} else {
					mask[i] = true
					i++
				}
			case c == '\'':
				inSingle = true
				mask[i] = false
				i++
			case c == '"':
				inDouble = true
				mask[i] = false
				i++
			default:
				mask[i] = true
				i++
			}
		}
		out = append(out, gocacheScriptLine{Text: line, IsCode: mask})
	}
	return out
}

// gocacheScriptMask 按文件类型给源码打掩码。
func gocacheScriptMask(rel string, code []string) []gocacheScriptLine {
	if strings.HasSuffix(rel, ".py") {
		return gocacheScriptMaskPython(code)
	}
	return gocacheScriptMaskShell(code)
}

// gocacheScriptValueEnd 返回【取值】在 Text 里的结束位置。
//
// ★ ★★ **为什么按「下一个赋值」切而不是按行尾切**：★★ 同一行上常有**另一个**变量：
// ★ `GOCACHE="$GOCACHE" GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"` ⇒ ⇒ ★★
// ★ ★★ ⇒ 若按行尾切 ⇒ ⇒ ★★ ⇒ 取值里会混进 **GOMODCACHE** 的 `$HOME`
// ⇒ ⇒ ★★ ⇒ ⇒ **假阳性**（★ 本轮实测踩到过）⇒ ⇒ ★★ ⇒ ⇒ 所以按「下一个 `名字=`」切。
func gocacheScriptValueEnd(text string, from int) int {
	for i := from; i < len(text); i++ {
		if i == 0 || !(text[i-1] == ' ' || text[i-1] == '\t') {
			continue
		}
		j := i
		for j < len(text) && (text[j] == '_' ||
			(text[j] >= 'a' && text[j] <= 'z') ||
			(text[j] >= 'A' && text[j] <= 'Z') ||
			(text[j] >= '0' && text[j] <= '9')) {
			j++
		}
		if j == i || j >= len(text) || text[j] != '=' {
			continue
		}
		// ★ 排除 `==` / `!=` / `<=` / `>=`（★ 那是比较，★ 不是赋值）。
		if j+1 < len(text) && text[j+1] == '=' {
			continue
		}
		if j > 0 && (text[j-1] == '=' || text[j-1] == '!' || text[j-1] == '<' || text[j-1] == '>') {
			continue
		}
		return i
	}
	return len(text)
}

// gocacheScriptVarAssignments 找出文件里【真正在代码里】的某个变量的赋值点。
//
// ★★★★ **为什么要掩码 + 跨行状态**（★ r1048 轮实测逼出来的，★ ★★ **这一条最值钱**）：★★★
// ★★★★ 一行里出现 `GOCACHE=` 有三种完全不同的含义：★★★
// ★★★★ ① 它**就是**一次赋值（★ 代码）；② 它**在某个字符串字面量里**（★ 数据）；
// ★★★★ ③ 它**在某个【跨行】的字符串里**（★ ★★ Python 的 docstring ★★）。
// ★★★★ ⇒ ⇒ ★★★★ ⇒ **不分就会误伤**（★ ★★ 实测两次：★ ① r1047 变异台的 M04 臂载荷
// ★★★★ ⇒ ⇒ ★★ ⇒ `M04_REPL = 'export GOCACHE="$HOME/.cache/go-build" …'`；★ ★★ ② 本轮
// ★★★★ ⇒ ⇒ ★★ ⇒ **自己**的变异台 docstring 里那句 `export GOCACHE="$HOME/.cache/go-build"`
// ★★★★ ⇒ ⇒ ★★ ⇒ ⇒ **分不清 ⇒ ⇒ 门就会误伤【治病的工具自己】** ⇒ ⇒ ★★★★ ⇒ ⇒
// ★★★★ ⇒ ⇒ **这是 r1047 审计档 §2.2 记过那条同族现象的第 N 次复现** ⇒ ⇒ ★★★★ ⇒ ⇒
// ★★★★ ⇒ ⇒ ⇒ **⇒ 修法在门里（门必须能分清「数据」与「代码」）** ⇒ ⇒ ★★★★ ⇒ ⇒ ⇒
// ★★★★ ⇒ ⇒ ⇒ **而不是靠「载荷写得不巧」**。
func gocacheScriptVarAssignments(rel string, code []string, varName string) []gocacheGocacheAssign {
	masked := gocacheScriptMask(rel, code)
	var out []gocacheGocacheAssign
	for li, ml := range masked {
		for j := 0; j+len(varName) <= len(ml.Text); j++ {
			if !ml.IsCode[j] {
				continue
			}
			if !strings.HasPrefix(ml.Text[j:], varName) {
				continue
			}
			// ★ 变量名本身必须**完整在代码里**（★ 否则会撞上更长名字的前缀）。
			k := j + len(varName)
			if k < len(ml.IsCode) && ml.IsCode[k] && isIdentChar(ml.Text[k]) {
				continue
			}
			for k < len(ml.Text) && (ml.Text[k] == ' ' || ml.Text[k] == '\t') {
				if !ml.IsCode[k] {
					break
				}
				k++
			}
			if k >= len(ml.Text) || ml.Text[k] != '=' {
				continue
			}
			k++
			end := gocacheScriptValueEnd(ml.Text, k)
			out = append(out, gocacheGocacheAssign{
				Line: li + 1,
				Rest: strings.TrimSpace(ml.Text[k:end]),
				Text: strings.TrimSpace(ml.Text),
			})
			j = k
		}
	}
	return out
}

// isIdentChar 判是不是标识符字符。
func isIdentChar(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// gocacheScriptIsUserGlobalGoCacheValue 判「这个 GOCACHE 赋值指向仓外用户目录」。
//
// ★ ★★ **为什么 `/tmp/` 开头的值不算**：★★ 那是**容器自己**的缓存
// ★ ★★ （★ 本仓的容器腿一律 `-e GOCACHE=/tmp/go-build-cache`）⇒ ⇒ ★★
// ★ ★★ ⇒ 而且容器是 `--rm` ⇒ ⇒ ★★ ⇒ **本来就是隔离的** ⇒ ⇒ ★★ ⇒ 不该被骂。
func gocacheScriptIsUserGlobalGoCacheValue(rest string) bool {
	t := strings.TrimSpace(rest)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "/tmp/") || strings.HasPrefix(t, "/var/tmp/") {
		return false
	}
	return gocacheScriptUserGlobalValueRE.MatchString(t)
}

// ── ★★ 扫描结果 ──

// gocacheScriptFile 是一个【被扫到的脚本文件】。
type gocacheScriptFile struct {
	// Rel 是【相对仓根】的路径。
	Rel string
	// Code 是【去掉整行注释】后的代码行（★ shell 的 `#` 与 python 的 `#` 同形）。
	Code []string
	// Excluded 标记它是否落在登记簿的排除路径里。
	Excluded bool
}

// gocacheScriptHasCompile 判「这个文件有没有【编译型】go 调用」。
func gocacheScriptHasCompile(code []string) bool {
	for _, l := range code {
		if gocacheScriptCompileRE.MatchString(l) || gocacheScriptCompileListRE.MatchString(l) {
			return true
		}
	}
	return false
}

// gocacheScriptScan 扫出仓内全部 `.sh` / `.py`，并按登记簿标注排除路径。
func gocacheScriptScan(root string, excluded map[string]bool) ([]gocacheScriptFile, error) {
	var out []gocacheScriptFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if path != root && gocacheScriptInfraSkipDirs[base] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".sh") && !strings.HasSuffix(name, ".py") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		isExcluded := false
		for prefix := range excluded {
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				isExcluded = true
				break
			}
		}
		out = append(out, gocacheScriptFile{
			Rel:      rel,
			Code:     gocacheCodeLines(string(raw)),
			Excluded: isExcluded,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// gocacheScriptDedicatedLines 返回【指向专用目录】的代码行。
func gocacheScriptDedicatedLines(code []string) []string {
	var out []string
	for _, l := range code {
		if gocacheScriptDedicatedRE.MatchString(l) {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// gocacheScriptUnconditionalDedicatedLines 返回【**无条件**指向专用目录】的代码行。
//
// ★ ★★ **为什么要把「条件式」剔掉**：★★ `GOCACHE="${GOCACHE:-$(…/gocache.sh …)}"`
// ★ ★★ ⇒ ⇒ 条件式 ⇒ ⇒ 用户环境里已设 ⇒ ⇒ 悄悄回落用全局缓存 ⇒ ⇒ ★★
// ★ ★★ ⇒ ⇒ **门绿着，而隔离根本没生效** ⇒ ⇒ ★★ ⇒ ⇒ 判据①c 靠它。
func gocacheScriptUnconditionalDedicatedLines(code []string) []string {
	var out []string
	for _, l := range gocacheScriptDedicatedLines(code) {
		if gocacheScriptSetdefaultRE.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// gocacheScriptNonExportGocacheLines 返回【非 export 形态】的 GOCACHE 赋值行。
//
// ★ ★★ **为什么单列这个口径**：★★ `env -i` **清空环境** ⇒ ⇒ ★★ 顶部那个
// ★★ `export GOCACHE` **被抹掉** ⇒ ⇒ ★★ ⇒ 必须有一个**显式列进 env 数组**的
// ★★ ⇒ ⇒ GOCACHE 赋值才活得下来（★ ⇒ ⇒ 判据② 靠它）。
func gocacheScriptNonExportGocacheLines(code []string) []string {
	var out []string
	for _, l := range code {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "export ") || strings.HasPrefix(t, "#") {
			continue
		}
		if gocacheScriptGocacheAssignRE.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

// gocacheScriptInScope 挑出【在作用域内】且【含编译型 go 调用】的文件。
func gocacheScriptInScope(files []gocacheScriptFile) []gocacheScriptFile {
	var out []gocacheScriptFile
	for _, f := range files {
		if f.Excluded {
			continue
		}
		if !gocacheScriptHasCompile(f.Code) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ── ★★ 判据①③：覆盖面 + 证据形态 + 条件式不算 ★─

// TestScriptFamilyCompileSitesUseDedicatedGoCache —— ★★ 判据①（★ 含 ①b 证据形态、★ ①c 条件式不算）：
// ★ 凡是调【编译型】go 子命令的脚本 ⇒ ⇒ 必须把 `GOCACHE` 指向【专用目录】。
//
// ★ ★★ **为什么【不分「宿主腿 / 容器腿」】**：★★ ★★ 容器腿（★ `docker run … -e
// ★★ GOCACHE=/tmp/…`）**本来就隔离**（★ 容器自带缓存 + `--rm`）⇒ ⇒ ★★ ⇒ 但要把它
// ★★ **判准**，就得**解析 shell 的引号与跨行结构** ⇒ ⇒ ★★ ⇒ ★★ ⇒ **判据会从「形态」
// ★★ ⇒ ★★ ⇒ 掉进「解析」⇒ ⇒ ★★ ⇒ ★★ ⇒ **假绿与假红都会来** ⇒ ⇒ ★★ ⇒
// ★★ ⇒ **宁可多要求一点**（★ 容器腿那个脚本多设一个 GOCACHE，★ 功能上无害）
// ★★ ⇒ ⇒ ★★ ⇒ **也不要「按子命令/按腿分类」带来的漏判**（★ r1047 第一面已定这条口径）。
func TestScriptFamilyCompileSitesUseDedicatedGoCache(t *testing.T) {
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}
	inScope := gocacheScriptInScope(files)

	// ★★ **搜索面完整性自检**（★ 与仓内其它门同款）。
	if len(files) == 0 {
		t.Fatalf("★ 一个脚本都没扫到 ⇒ ⇒ 【搜索面不完整】⇒ ⇒ 本门是瞎的")
	}
	if len(inScope) == 0 {
		t.Fatalf("★ 一个【在作用域内、且调编译型 go 命令】的脚本都没扫到 ⇒ ⇒ " +
			"【搜索面不完整】⇒ ⇒ 本门是瞎的")
	}
	if len(inScope) < reg.MinCompileFiles {
		t.Fatalf("★ 扫到 %d 个【在作用域内的编译型脚本】，但登记簿要求至少 %d 个 ⇒ ⇒ "+
			"【搜索面被削掉了一块】⇒ ⇒ 本门此刻**盖不住**全量 ⇒ ⇒ "+
			"要么有脚本被移走/改名了，要么扫描口径变了 ⇒ ⇒ 重新审一遍",
			len(inScope), reg.MinCompileFiles)
	}

	var noEvidence, onlySetdefault []string
	for _, f := range inScope {
		dedicated := gocacheScriptDedicatedLines(f.Code)
		if len(dedicated) == 0 {
			noEvidence = append(noEvidence, f.Rel)
			continue
		}
		if len(gocacheScriptUnconditionalDedicatedLines(f.Code)) == 0 {
			onlySetdefault = append(onlySetdefault, f.Rel)
		}
	}

	if len(noEvidence) > 0 || len(onlySetdefault) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "★ %d 个【调编译型 go 命令】的脚本**没有**把 GOCACHE 指向专用目录"+
			"（★ 在作用域内的编译型脚本共 %d 个）：\n",
			len(noEvidence)+len(onlySetdefault), len(inScope))
		for _, s := range noEvidence {
			fmt.Fprintf(&b, "  ★ %s（★ 压根没指向专用目录）\n", s)
		}
		for _, s := range onlySetdefault {
			fmt.Fprintf(&b, "  ★ %s（★ **只有条件式**的指向 ⇒ ⇒ 用户环境里已设 GOCACHE ⇒ ⇒ "+
				"悄悄回落用全局缓存 ⇒ ⇒ 门绿着而隔离没生效）\n", s)
		}
		b.WriteString("★ ⇒ ⇒ 它们调【编译型】go 命令 ⇒ ⇒ 子进程会往**用户的全局缓存**里写产物\n")
		b.WriteString("★ ⇒ ⇒ ⇒ ★ 本判据**不分宿主腿 / 容器腿** ⇒ ⇒ ★★ ⇒ 容器腿多设一个\n")
		b.WriteString("★ ⇒ ⇒ ⇒ ★ GOCACHE 功能上无害 ⇒ ⇒ ★★ ⇒ **宁可多要求，也不要漏判**\n")
		b.WriteString("★ ⇒ ⇒ ⇒ 实测（★ 2026-10-29，本机）：★ **单包** `go test ./admin/` 就要\n")
		b.WriteString("★ ⇒ ⇒ ⇒ **846 MB** ⇒ ⇒ ★★ 整模块带 tag 是 **997 MB** ⇒ ⇒ ★★ ⇒ 体积由\n")
		b.WriteString("★ ⇒ ⇒ ⇒ 【传递依赖闭包】决定 ⇒ ⇒ ★★ ⇒ **按「编译范围」分层不成立**\n")
		b.WriteString("★ ⇒ ⇒ ⇒ 修法：★ shell 用 `. <仓根>/scripts/lib/gocache.sh` + `gocache_use <purpose>`；\n")
		b.WriteString("★ ⇒ ⇒ ⇒ ★ Python 用 `gocache.apply_to_environ(\"<purpose>\")`")
		t.Fatal(b.String())
	}
}

// ── ★★ 判据②：`env -i` 会抹掉顶部的 export ──

// TestScriptFamilyIsolationSurvivesEnvDashI —— ★★ 判据②：
// ★ 用了 `env -i` 的脚本 ⇒ ⇒ 必须把 GOCACHE **显式列进**那个 env（★ 不然顶部的
// ★ export 被清空 ⇒ ⇒ ⇒ 隔离失效，而门是绿的）。
func TestScriptFamilyIsolationSurvivesEnvDashI(t *testing.T) {
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}
	inScope := gocacheScriptInScope(files)

	var checked []string
	var offenders []string
	for _, f := range inScope {
		usesEnvDashI := false
		for _, l := range f.Code {
			if gocacheScriptEnvDashI_RE.MatchString(l) {
				usesEnvDashI = true
				break
			}
		}
		if !usesEnvDashI {
			continue
		}
		checked = append(checked, f.Rel)

		// ★ 找【非 export 形态】的 GOCACHE 赋值。
		//
		// ★ ★★ **两条都算隔离成立**：★★
		// ★ ① 那一行**自己**指向专用目录（★ `GOCACHE="$("$(…/gocache.sh" script)"`）；
		// ★ ② 那一行是**原样带出**（★ `GOCACHE="$GOCACHE"`）⇒ ⇒ ★★ 但这**有前提**：
		// ★★ 本文件别处必须有【**无条件**】的专用指向 ⇒ ⇒ ★★ ⇒ **那个被引用的
		// ★★ `$GOCACHE` 才真的是专用目录** ⇒ ⇒ ★★ ⇒ 隔离才成立。
		fileHasDedicated := len(gocacheScriptUnconditionalDedicatedLines(f.Code)) > 0
		ok := false
		for _, l := range gocacheScriptNonExportGocacheLines(f.Code) {
			if gocacheScriptSetdefaultRE.MatchString(l) {
				continue
			}
			if gocacheScriptDedicatedRE.MatchString(l) {
				ok = true
				break
			}
			if fileHasDedicated && gocacheScriptPassthroughRE.MatchString(l) {
				ok = true
				break
			}
		}
		if !ok {
			offenders = append(offenders, f.Rel)
		}
	}

	// ★★ **搜索面完整性自检**：★ 一个用 `env -i` 的脚本都没扫到 ⇒ ⇒ 这条判据是瞎的
	// ★★ ⇒ ⇒ **要么是判据失效了，要么是脚本形态变了** ⇒ ⇒ 两种都得知道。
	if len(checked) == 0 {
		t.Fatalf("★ 没有任何【在作用域内、且调编译型 go 命令】的脚本用 `env -i` ⇒ ⇒ " +
			"【本判据当前无适用对象】⇒ ⇒ 这不是「没问题」，是「**判据没被覆盖到**」⇒ ⇒ " +
			"要么仓里已经没有这种形态（★ 那本判据可以退役），要么扫描口径变了 ⇒ ⇒ 重新审一遍")
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("★ %d 个脚本用了 `env -i`（★ 它会**清空环境**）⇒ ⇒ 顶部那个 `export GOCACHE`"+
			"**被抹掉** ⇒ ⇒ ⇒ 隔离失效，而门是绿的（★ 「设了」≠「用上了」）：\n  %s\n"+
			"★ ⇒ ⇒ 修法：★ 把 GOCACHE **显式列进**那个 env 数组（★ 例：`BASE_ENV=(… GOCACHE=…)`）\n"+
			"★ ⇒ ⇒ ⇒ 并且必须是【无条件】的（★ `GOCACHE=\"${GOCACHE:-…}\"` 仍是条件式 ⇒ 照样回落）",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// ── ★★ 判据③：不许「同时」有一处指向仓外用户目录的 GOCACHE ──

// TestScriptFamilyHasNoUserGlobalGoCacheOverride —— ★★ 判据③：
// ★ 一个文件**同时**有「正确的专用隔离」与「一处指向仓外用户目录的 GOCACHE」
// ★ ⇒ ⇒ ⇒ **后者会覆盖前者** ⇒ ⇒ ⇒ 「有没有专用证据」这个判据会**判绿** ⇒ ⇒ ⇒
// ★ ⇒ ⇒ **假绿** ⇒ ⇒ ⇒ 所以这里单独盯住那一处。
func TestScriptFamilyHasNoUserGlobalGoCacheOverride(t *testing.T) {
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}

	var offenders []string
	for _, f := range files {
		if !gocacheScriptHasCompile(f.Code) {
			continue
		}
		for _, a := range gocacheScriptVarAssignments(f.Rel, f.Code, "GOCACHE") {
			if gocacheScriptIsUserGlobalGoCacheValue(a.Rest) {
				offenders = append(offenders, fmt.Sprintf("%s:%d（★ %s）", f.Rel, a.Line, a.Text))
				break
			}
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("★ %d 处把 **GOCACHE** 指向了【仓外的用户目录】（★ 而这些文件**同时**有专用隔离）：\n  %s\n"+
			"★ ⇒ ⇒ **那一处会覆盖掉**前面刚设好的专用目录 ⇒ ⇒ ⇒ 「有没有专用证据」会**判绿**\n"+
			"★ ⇒ ⇒ ⇒ ⇒ **假绿**（★ 与 r1047 的 M04 臂同族，★ 但形态更刁：★ M04 是「**只有**错的那一处」，\n"+
			"★ ⇒ ⇒ ⇒ ⇒ 这一条是「**两处都有**」⇒ ⇒ ★★ ⇒ ★★ ⇒ 所以必须**单独**盯住这一处）\n"+
			"★ ⇒ ⇒ ⇒ 修法：★ 删掉那一处（★ 前导块已经无条件把 GOCACHE 指向专用目录了）",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// ── ★★ 判据④：GOMODCACHE 不许进 `.build-local/` ──

// TestGomodcacheMustNotLiveUnderBuildLocal —— ★★ 判据④：
// ★ 模块缓存**内容寻址且可复用** ⇒ ⇒ 它不是垃圾 ⇒ ⇒ **不该被隔离**；
// ★ 而隔离它 ⇒ ⇒ `git clean -xfd` 会删掉它 ⇒ ⇒ 每次都要重下整个模块图 ⇒ ⇒ 更糟。
func TestGomodcacheMustNotLiveUnderBuildLocal(t *testing.T) {
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}

	var offenders []string
	for _, f := range files {
		for _, a := range gocacheScriptVarAssignments(f.Rel, f.Code, "GOMODCACHE") {
			if strings.Contains(a.Rest, ".build-local") {
				offenders = append(offenders, fmt.Sprintf("%s:%d（★ %s）", f.Rel, a.Line, a.Text))
				break
			}
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("★ %d 处把 **GOMODCACHE** 指向了 `.build-local/` 下：\n  %s\n"+
			"★ ⇒ ⇒ 模块缓存是**内容寻址且可复用**的 ⇒ ⇒ 它**不是垃圾** ⇒ ⇒ **不该被隔离**\n"+
			"★ ⇒ ⇒ ⇒ 而隔离它 ⇒ ⇒ `git clean -xfd` 会把它删掉 ⇒ ⇒ ⇒ 每次都要**重下整个模块图**\n"+
			"★ ⇒ ⇒ ⇒ ⇒ 那比不隔离**更糟**（★ 慢 + 费网 + 离线环境直接挂）\n"+
			"★ ⇒ ⇒ ⇒ 修法：★ GOMODCACHE 留在**用户级**位置（★ 如 `$HOME/go/pkg/mod`）",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// ── ★★ 判据⑤：排除清单必须逐项核实 ──

// TestScriptFamilyExclusionRegistryIsExact —— ★★ 判据⑤：
// ★ 登记簿里每一条排除路径 ⇒ ⇒ 它的【编译型脚本文件数】必须与登记值**逐字相等**。
//
// ★ ★★ **为什么必须逐字相等**：★★ 排除是**政策** ⇒ ⇒ 政策会漂 ⇒ ⇒ ★★
// ★★ **「新加一个 go 调用却没人发现」是排除清单最典型的失效形态** ⇒ ⇒ ★★
// ★★ ⇒ ⇒ 任何新增都会让门红 ⇒ ⇒ ★★ ⇒ **逼着人重新审一遍**。
func TestScriptFamilyExclusionRegistryIsExact(t *testing.T) {
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}

	// ★ 实测：每条排除路径下的【编译型脚本文件数】。
	actual := map[string]int{}
	for _, f := range files {
		if !f.Excluded {
			continue
		}
		if !gocacheScriptHasCompile(f.Code) {
			continue
		}
		for _, e := range reg.ExcludedPaths {
			if f.Rel == e.Path || strings.HasPrefix(f.Rel, e.Path+"/") {
				actual[e.Path]++
				break
			}
		}
	}

	var problems []string
	for _, e := range reg.ExcludedPaths {
		if strings.TrimSpace(e.Reason) == "" {
			problems = append(problems, fmt.Sprintf(
				"★ 排除路径 %s **没有写理由** ⇒ ⇒ 登记簿自己失效（★ 谁都能随手排除）", e.Path))
		}
		if e.CompileFiles != actual[e.Path] {
			problems = append(problems, fmt.Sprintf(
				"★ 排除路径 %s 的【编译型脚本文件数】登记值 %d ≠ 实测 %d ⇒ ⇒ "+
					"★ 要么有新增没人发现，★ 要么登记值过期（★ 脚本被删/改名了）⇒ ⇒ 重新审一遍并更新登记簿",
				e.Path, e.CompileFiles, actual[e.Path]))
		}
	}

	// ★★ **反向**：登记簿里**实际已不存在**的路径 ⇒ ⇒ 是过期登记。
	// ★ ★★ **注意判据的口径**：★★ 这里查的是【**目录还在不在**】，★★ 而**不是**
	// ★ ★★ 「有没有扫到 .sh / .py」⇒ ⇒ ★★ 因为一个目录**完全可以**一个脚本都没有
	// ★ ★★ （★ 实测：`web-mobile/` 有 554 个追踪文件、★ 但一个 `.sh` / `.py` 都没有）
	// ★ ★★ ⇒ ⇒ ★★ 若用「扫到文件」当口径 ⇒ ⇒ ★★ ⇒ **会把这种目录误判成过期登记**
	// ★ ★★ ⇒ ⇒ ★★ ⇒ **量具指错对象** ⇒ ⇒ ★★ ⇒ 门就会给出假红。
	for _, e := range reg.ExcludedPaths {
		if _, err := os.Stat(filepath.Join(root, e.Path)); err != nil {
			problems = append(problems, fmt.Sprintf(
				"★ 排除路径 %s **在仓里已经不存在了** ⇒ ⇒ 是过期登记 ⇒ ⇒ 把它删掉（★ "+
					"过期登记会静默地不再覆盖真实存在的东西）", e.Path))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("★ 排除清单登记簿（%s）与实测**不一致**：\n  %s\n"+
			"★ ⇒ ⇒ 排除是【政策】⇒ ⇒ 政策会漂 ⇒ ⇒ ★★ ⇒ **「新增没人发现」与「登记值过期」\n"+
			"★ ⇒ ⇒ ⇒ 是排除清单最典型的两种失效形态** ⇒ ⇒ ⇒ 门在这里盯着它们",
			gocacheScriptExclusionRegistryRel, strings.Join(problems, "\n  "))
	}
}
