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
	"os/exec"
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
	Note                 string                        `json:"note"`
	MinCompileFiles      int                           `json:"minCompileFiles"`
	MeasuredCompileFiles int                           `json:"measuredCompileFiles"`
	ExcludedPaths        []gocacheScriptExclusionEntry `json:"excludedPaths"`
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
//
// ★ ★★★★ **r1049 审计轮移除的一条（★ `gocache\.dir_for(`）**：★★★★
// ★ ★★★★ `dir_for()` **只算路径**：★★ 它**不建目录**、★★ **不查可写** ⇒ ⇒ ★★
// ★ ★★★★ ⇒ ★★ 而 go 拿到一个**不可用**的 GOCACHE 会直接报
// ★ ★★★★ `failed to initialize build cache` ⇒ ⇒ ★★ ⇒ **脚本直接坏掉** ⇒ ⇒ ★★
// ★ ★★★★ ⇒ ★★ ⇒ 而本正则原先把它认作「已指向专用缓存」⇒ ⇒ ★★ ⇒ **门绿着，
// ★ ★★★★ 而脚本在受限环境下会坏** ⇒ ⇒ ★★ ⇒ ★★ ⇒ **这是 r1048 第一原则
// ★ ★★★★ （「宁可漏隔离，也不许把脚本弄坏」）的正面破口** ⇒ ⇒ ★★ ⇒ ★★ ⇒
// ★ ★★★★ ⇒ 移出去 ⇒ ⇒ ★★ ⇒ 只认**真正失败安全**的入口
// ★ ★★★★ （★ `gocache_use` / `gocache.env(` / `gocache.apply_to_environ(`）。
var gocacheScriptDedicatedRE = regexp.MustCompile(
	`\.build-local/\.gocache-|gocache\.sh|gocache_use|gocache\.env\(|gocache\.apply_to_environ\(`)

// gocacheScriptDirForGocacheRE 认【把 GOCACHE 设成「只算路径」的结果】的形态。
//
// ★ ★★ **为什么它必须存在（★ r1049 审计轮）**：★★ 上面的 `gocacheScriptDedicatedRE`
// ★ ★★ 把 `dir_for(` 踢出去之后 ⇒ ⇒ ★★ 「一个文件**只**用 `dir_for` 设 GOCACHE」
// ★ ★★ 就**没有任何专用证据**了 ⇒ ⇒ ★★ 判据① 会**自动**红 ⇒ ⇒ ★★ ⇒ ★★ ⇒ 那就够了吗？
// ★ ★★ ⇒ ⇒ ★★ **不够**：★★ ★★ 判据① 是**文件级**的 ⇒ ⇒ ★★ 一个文件里
// ★ ★★ 「既有 `apply_to_environ`（★ 合格）又有 `dir_for`（★ 不合格）」⇒ ⇒ ★★
// ★ ★★ 判据① 仍然绿 ⇒ ⇒ ★★ ⇒ ★★ ⇒ **不合格的那一处会被漏掉** ⇒ ⇒ ★★ ⇒ ★★ ⇒
// ★ ★★ ⇒ 所以要**单独**盯住这一处（★ 与判据③「不许同时有一处指向用户目录」同款）。
var gocacheScriptDirForGocacheRE = regexp.MustCompile(
	`GOCACHE[^\n=]{0,4}=[^\n]*gocache\.dir_for\(`)

// gocacheScriptSetdefaultRE 认【条件式】的 GOCACHE 设定。
//
// ★ ★★ **为什么单列一条**：★★ 条件式 ⇒ ⇒ 用户环境里已设 ⇒ ⇒ 悄悄回落
// ★ ★★ ⇒ ⇒ ⇒ **门绿着，而隔离根本没生效** ⇒ ⇒ ★★ ⇒ 所以它**不算隔离证据**。
var gocacheScriptSetdefaultRE = regexp.MustCompile(
	`\$\{GOCACHE:-|setdefault\(\s*["']GOCACHE["']|GOCACHE["']\]\s*=\s*os\.environ\.get\(`)

// gocacheScriptGocacheAssignRE 认【任何形态】的 GOCACHE 赋值。
var gocacheScriptGocacheAssignRE = regexp.MustCompile(`\bGOCACHE\s*=`)

// gocacheScriptSubscriptGocacheRE 认【Python 下标形态】的 GOCACHE 赋值，
// 且【取值】指向仓外的用户目录。
//
// ★ ★★ **为什么它必须单列（★ r1049 审计轮）**：★★ 判据③ 的主检测器跑在
// ★ ★★ 【普通字符串算数据】的掩码上 ⇒ ⇒ ★★ ⇒ 而 `os.environ["GOCACHE"] = …`
// ★ ★★ 里的 `"GOCACHE"` **是字符串** ⇒ ⇒ ★★ ⇒ **整条赋值对它不可见** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ **判据③ 只对 shell 形态有效** ⇒ ⇒ ★★ ⇒ 一个 Python 脚本写
// ★ ★★ `os.environ["GOCACHE"] = os.path.expanduser("~/.cache/go-build")`
// ★ ★★ ⇒ ⇒ ★★ ⇒ **门照样绿** ⇒ ⇒ ★★ ⇒ **那是假绿** ⇒ ⇒ ★★ ⇒ ★★ ⇒
// ★ ★★ ⇒ 这里补一条**窄**判据：★ 【下标形态】＋ ★ 【取值】指向用户目录。
//
// ★ ★★ **为什么窄**：★★ 宽了就会误伤【变异台的载荷字符串】（★ 载荷是**数据**
// ★ ★★ ⇒ ⇒ 按仓内既有约定「字符串里的 `GOCACHE=` 不是赋值」⇒ ⇒ ★★ ⇒ 不算）★★
// ★ ★★ ⇒ ⇒ ★★ ⇒ 而本仓现存的载荷**都是 shell 形态** ⇒ ⇒ ★★ ⇒ 窄判据够用。
var gocacheScriptSubscriptGocacheRE = regexp.MustCompile(
	`\w+(?:\.\w+)*\s*\[\s*['"]GOCACHE['"]\s*\]\s*=\s*[^\n]*` +
		`(\$\{?HOME\b|~/|expanduser|go-build|sdk/gocache|Library/Caches)`)

// gocacheScriptSubscriptGomodcacheRE 认【Python 下标形态】的 GOMODCACHE 赋值，
// 且【取值】落在 `.build-local/` 下（★ 判据④ 的同款盲区）。
var gocacheScriptSubscriptGomodcacheRE = regexp.MustCompile(
	`\w+(?:\.\w+)*\s*\[\s*['"]GOMODCACHE['"]\s*\]\s*=\s*[^\n]*\.build-local`)

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
				// ★ ★★ **r1049 审计轮修的**：★ 原来这里写的是 `inTriple = "''"`
				// ★ ★★ （★ **两个**引号 ★★）⇒ ⇒ ★★ ⇒ 闭合时找的是 `''` ⇒ ⇒ ★★ ⇒
				// ★ ★★ ⇒ 块**内部**只要出现 `''`（★ 例：★ `don''t` ★ 或一段
				// ★ ★★ 空串）⇒ ⇒ ★★ ⇒ **提前闭合** ⇒ ⇒ ★★ ⇒ 块后面的代码被当成
				// ★ ★★ 数据 ⇒ ⇒ ★★ ⇒ **又一处错位** ⇒ ⇒ ★★ ⇒ 定界符必须是
				// ★ ★★ **三个**引号 ★★。
				inTriple = `'''`
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
	// Code 是【未经任何剥离】的原始行（★ ★★ 行号与文件一一对应 ★★）。
	//
	// ★ ★★ **r1049 审计轮改的（★ 原来这里是「剥掉整行注释」的行）**：★★
	// ★ ★★ **「先剥整行注释、再打掩码」这个次序本身是错的** ⇒ ⇒ ★★ ⇒ 因为
	// ★ ★★ Python 里三引号块**内部**的 `#` **不是注释** ⇒ ⇒ ★★ ⇒ 一段三引号
	// ★ ★★ 字符串的**闭合定界符**若落在某条「以 `#` 开头的行」上 ⇒ ⇒ ★★ ⇒ 那行
	// ★ ★★ 会被「剥整行注释」**删掉** ⇒ ⇒ ★★ ⇒ **闭合符消失** ⇒ ⇒ ★★ ⇒ 掩码的
	// ★ ★★ 状态机从此错位 ⇒ ⇒ ★★ ⇒ **该文件后半段全被当成数据** ⇒ ⇒ ★★ ⇒
	// ★ ★★ ⇒ 它调的那些 go 命令**门看不见** ⇒ ⇒ ★★ ⇒ **假绿**（★ 实测：
	// ★ ★★ ⇒ `scripts/mutate-r164-freshness-gate.py` 的 `["go", "test", …]`
	// ★ ★★ ⇒ ⇒ 在门眼里**根本不存在** ⇒ ⇒ ⇒ 作用域少算 1 个 ⇒ ⇒ ⇒ 是本轮
	// ★ ★★ ⇒ ★★ **独立量具交叉对拍**抓出来的（★ 不是门自己抓到的）★★）。
	// ★ ★★ ⇒ ⇒ 所以顺序必须是【先打掩码（★ 掩码自己认注释）⇒ ⇒ 再取代码视图】。
	Code []string
	// Excluded 标记它是否落在登记簿的排除路径里。
	Excluded bool
}

// ── ★★ 编译型判定用的掩码（★ r1049 审计轮新增）★★

// gocacheScriptCompileMask 给源码打【编译型判定用】的掩码。
//
// ★ ★★ **规则（三档）**：★★
// ★ ① **注释** ⇒ ⇒ 数据（★ 不是代码）；
// ★ ② **三引号块**（★ Python 的 docstring / 多行字符串）⇒ ⇒ 数据（★ 散文，不是调用）；
// ★ ③ **其余一律算代码**（★ ★★ 含**普通字符串** ★★ —— 因为
// ★ ★★ `["go", "test"]` 这种**列表形态的 go 调用**本身就在字符串里 ★★
// ★ ★★ ⇒ ⇒ 若把普通字符串也当数据 ⇒ ⇒ ★★ ⇒ 门会漏掉一半的 go 调用 ⇒ ⇒ ★★ ⇒ 危险）。
//
// ★ ★★ **为什么 ② 只认三引号**：★★ 普通字符串里的散文（如 `"go test failed"`）
// ★ ★★ 会造成**虚高** ⇒ ⇒ 但那是【安全的方向】（★ 多要求 ⇒ ⇒ 假红 ⇒ 不漏判）
// ★ ★★ ⇒ ⇒ ★★ 而三引号块里的 `go test ./...` 是**成段散文** ⇒ ⇒ ★★ 命中率最高 ⇒ ⇒
// ★ ★★ ⇒ 优先在那一层消掉虚高（★ 实测：★ 3 个文件 ⇒ ★ `scripts/lib/gocache.py`
// ★ ★★ / `scripts/perf/bench_compare.py` / `scripts/audit/mutate_r1047_gocache_isolation_rollout.py`）。
//
// ★ ★★ **与 `gocacheScriptMaskPython` 的区别（★ 别混用）**：★★ 那一个把
// ★ ★★ **普通字符串也当数据**（★ 因为它服务的是「这个 GOCACHE 赋值是不是真赋值」
// ★ ★★ ⇒ ⇒ 字符串里的 `GOCACHE=` 不是赋值）★★ ⇒ ⇒ **两个掩码服务两个问题**
// ★ ★★ ⇒ ⇒ ★★ ⇒ **各写各的** ⇒ ⇒ ★★ ⇒ **别为了省代码复用错的那个**。
func gocacheScriptCompileMask(rel string, code []string) []gocacheScriptLine {
	out := make([]gocacheScriptLine, 0, len(code))
	isPy := strings.HasSuffix(rel, ".py")
	inTriple := ""
	inSingle, inDouble := false, false
	for _, line := range code {
		mask := make([]bool, len(line))
		i := 0
		for i < len(line) {
			// ★ 在跨行三引号块里 ⇒ ⇒ 直到闭合定界符都是数据。
			if inTriple != "" {
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
			if isPy {
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
					inTriple = `'''`
					for j := 0; j < 3 && i+j < len(line); j++ {
						mask[i+j] = false
					}
					i += 3
				case c == '\'' || c == '"':
					// ★ 普通字符串 ⇒ ⇒ **算代码**（★ 见函数头规则③），
					// ★ ★★ 但仍要跟踪转义与闭合 ⇒ ⇒ ★★ ⇒ 否则字符串里的 `#`
					// ★ ★★ 会被当成注释 ⇒ ⇒ ★★ ⇒ 后面真正的代码会被误当数据。
					mask[i] = true
					i++
					for i < len(line) {
						if line[i] == '\\' && i+1 < len(line) {
							mask[i] = true
							mask[i+1] = true
							i += 2
							continue
						}
						mask[i] = true
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
				continue
			}
			// ── shell ──
			if inSingle || inDouble {
				mask[i] = true
				if inDouble && line[i] == '\\' && i+1 < len(line) {
					mask[i+1] = true
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
			switch {
			case c == '#':
				// ★ 词首的 `#` 才是注释（★ 否则 `a#b` 是一个词）。
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
				mask[i] = true
				i++
			case c == '"':
				inDouble = true
				mask[i] = true
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

// gocacheScriptCodeText 把掩码里的【数据】字节换成空格 ⇒ ⇒ 得到【只看代码】的文本。
func gocacheScriptCodeText(lines []gocacheScriptLine) []string {
	out := make([]string, 0, len(lines))
	for _, ml := range lines {
		b := []byte(ml.Text)
		for i := range b {
			if i < len(ml.IsCode) && !ml.IsCode[i] {
				b[i] = ' '
			}
		}
		out = append(out, string(b))
	}
	return out
}

// gocacheScriptCompileCode 返回【只看代码】的行（★ 注释与三引号块已被抹掉）。
func gocacheScriptCompileCode(rel string, code []string) []string {
	return gocacheScriptCodeText(gocacheScriptCompileMask(rel, code))
}

// gocacheScriptFullCodeText 用【完整掩码】（★ ★★ 普通字符串也算数据 ★★）产出
// 【只看代码】的文本。
//
// ★ ★★ **它与 `gocacheScriptCompileCode` 的区别（★ 两个问题，两个掩码）**：★★
// ★ ★★ ① 「这个 `dir_for(` **调用**是不是真代码」★★ ⇒ ⇒ **普通字符串是数据**
// ★ ★★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ （★ 变异台的【载荷字符串】里的 `gocache.dir_for(` ⇒ ⇒ ★★
// ★ ★★ ⇒ ⇒ **不是调用** ⇒ ⇒ ★★ ⇒ 不该被算成「设了 GOCACHE」）★★；
// ★ ★★ ② 「这个 `["go", "test"]` **列表**是不是真调用」★★ ⇒ ⇒ **普通字符串是代码**
// ★ ★★ ⇒ ⇒ ★★ ⇒ （★ 否则会漏掉一半的 go 调用 ⇒ ⇒ 那是**危险方向**）。
func gocacheScriptFullCodeText(rel string, code []string) []string {
	masked := gocacheScriptMask(rel, code)
	out := make([]string, 0, len(masked))
	for _, ml := range masked {
		b := []byte(ml.Text)
		for i := range b {
			if i < len(ml.IsCode) && !ml.IsCode[i] {
				b[i] = ' '
			}
		}
		out = append(out, string(b))
	}
	return out
}

// gocacheScriptHasCompile 判「这个文件有没有【编译型】go 调用」。
//
// ★ ★★ **r1049 审计轮改的（★ 原来跑在【未剥注释】的 `f.Code` 上）**：★★
// ★ ★★ 原口径把【注释里】和【docstring 里】提到的 go 命令也当成调用 ⇒ ⇒ ★★
// ★ ★★ ⇒ 作用域虚高（★ 实测：★ 3 个文件只在 docstring 里提到 go 命令）⇒ ⇒ ★★
// ★ ★★ ⇒ 现在跑在【编译型掩码】上 ⇒ ⇒ ★★ ⇒ **注释与三引号块不算**。
//
// ★ ★★ **为什么这不是「漏判」**：★★ 掩码只抹掉【注释】与【三引号块】⇒ ⇒ ★★
// ★ ★★ ⇒ **真正的调用一定还在** ⇒ ⇒ ★★ ⇒ 方向是【少要求】⇒ ⇒ ★★ ⇒ 安全。
func gocacheScriptHasCompile(rel string, code []string) bool {
	for _, l := range gocacheScriptCompileCode(rel, code) {
		if gocacheScriptCompileRE.MatchString(l) || gocacheScriptCompileListRE.MatchString(l) {
			return true
		}
	}
	return false
}

// gocacheScriptScan 扫出仓内全部 `.sh` / `.py`，并按登记簿标注排除路径。
func gocacheScriptScan(root string, excluded map[string]bool) ([]gocacheScriptFile, error) {
	rels, err := gocacheScriptRepoFiles(root)
	if err != nil {
		return nil, err
	}
	out := make([]gocacheScriptFile, 0, len(rels))
	for _, rel := range rels {
		raw, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if rerr != nil {
			// ★ 文件在索引里、工作树里没有（★ `git rm --cached` / staged 删除）⇒ ⇒
			// ★ 它**不在工作树里**⇒ ⇒ 不进本门的扫描面 ⇒ ⇒ ★ **不是错误**。
			if os.IsNotExist(rerr) {
				continue
			}
			return nil, rerr
		}
		isExcluded := false
		for prefix := range excluded {
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				isExcluded = true
				break
			}
		}
		out = append(out, gocacheScriptFile{
			Rel:      rel,
			Code:     gocacheRawLines(string(raw)),
			Excluded: isExcluded,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// gocacheScriptRepoFiles 列出【git 认的】脚本候选（★ 相对仓根、斜杠分隔）。
//
// ★★★ **为什么扫描面是「git 认的」而不是「文件系统上走得到的」（★ 2026-10-31 审计轮）**：★★★
//
//	★ 原口径 = `filepath.WalkDir(仓根)` ⇒ ⇒ **扫的是【工作树文件系统】** ⇒ ⇒
//	★ ⇒ ⇒ **本地任何未跟踪文件都会进作用域** ⇒ ⇒ ⇒ 而这些文件：
//	★   ① **不属于本仓**（★ `.gitignore` 已经声明「它们不是仓的一部分」）⇒ ⇒
//	★   ② **逐机不同**（★ 同一个 commit，在 A 机绿、在 B 机红）⇒ ⇒ ⇒
//	★   ③ ⇒ ⇒ **让「实测计数逐字对拍」这条判据失去意义**（★ 登记的是 165，
//	★      实测却随本地残留漂到 166 / 168）⇒ ⇒ ⇒ ★★ **同一类事故已经咬过两次**：
//	★      ① 源仓的 `scripts/build/`（★ 未跟踪目录、★ 7/23 遗留）⇒ ⇒ 165 → 166；
//	★      ② syncfield-2 的 `.scratch/timer/`（★ 2026-07-17 的死定时器残留）⇒ ⇒ 165 → 168。
//	★ ⇒ ⇒ ⇒ 两次的处置都是【把本地残留搬走】⇒ ⇒ ★★ **而那只是止血**：
//	★ ⇒ ⇒ ⇒ **下一个会话再放一个残留进仓，红灯会再来一次** ⇒ ⇒ ⇒
//	★ ⇒ ⇒ ⇒ ★★ **⇒ 所以本轮改【口径】而不是再清一次**：★ 扫描面改成
//	★ ⇒ **【git 认的】**（★ `git ls-files --cached --others --exclude-standard`）⇒ ⇒
//	★ ⇒ ★★ 语义是：「**凡是被本仓的 `.gitignore` 声明为『不属于本仓』的文件，
//	★ ⇒ 就不进本门的计数**」⇒ ⇒ ⇒ ★★ 而这条政策**本来就是仓内写着的**
//	★ ⇒ ⇒ （★ `.gitignore`）⇒ ⇒ ⇒ ★ **不是本门新造的** ⇒ ⇒ ⇒ ★
//	★ ⇒ ★★ **同时**保留了「未跟踪但没被忽略」的文件（★ ⇒ 即将提交的新脚本仍看得见）⇒ ⇒
//	★ ⇒ ⇒ ⇒ ★ **「提前发现」的能力没丢** ⇒ ⇒ ⇒ ⇒ 只丢掉了【本就不属于仓内的】那些。
//
// ★★ **为什么不是「只扫被跟踪的」（`--cached`）**：★ 那会让「新增一个还没提交的编译型脚本」
// ★ 在提交前**完全不被看见**（★ 本地绿灯 ⇒ 提交后 CI 红灯）⇒ ⇒ ★★ 体验上是
// ★★ **「我本地是绿的，CI 是红的」** ⇒ ⇒ ⇒ ★ 所以保留 `--others --exclude-standard`
// ★ ⇒ ★★ （★ 未跟踪 **且** 未被忽略 ⇒ ⇒ 正是「我打算提交它」的语义）⇒ ⇒ ⇒ ★
// ★ ★★ **同时**保留了原口径的严格性（★ 未提交的新脚本仍进作用域）⇒ ⇒ ⇒ ★
func gocacheScriptRepoFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--full-name",
		"--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("★ 列 git 认的文件失败（★ 需要 git 在 PATH 且 %s 是仓根）：%w", root, err)
	}
	var rels []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		if !strings.HasSuffix(p, ".sh") && !strings.HasSuffix(p, ".py") {
			continue
		}
		if gocacheScriptHasInfraSkipDir(p) {
			continue
		}
		rels = append(rels, p)
	}
	return rels, nil
}

// gocacheScriptHasInfraSkipDir 判断路径里是否含【基础设施跳过目录】（★ 任意一层都算）。
//
// ★★★ **为什么必须逐层判**：★★★ ★ `filepath.WalkDir` 口径下，任何深度下叫
// ★ `vendor` / `node_modules` / `maestro` / `.build-local` / `.git` 的目录都被跳过
// ★ ⇒ ⇒ ★ 换成 git 枚举后**这些目录里的文件是会被列出来的**（★ 它们多数被跟踪）
// ★ ⇒ ⇒ ★★ **不逐层判 ⇒ 作用域会突然变大** ⇒ ⇒ ⇒ ★ ⇒ 实测：源仓的 `vendor/` 下有
// ★ ★ 数百个 `.sh` ⇒ ⇒ ★★ 不判 ⇒ 计数会从 165 跳到 500+ ⇒ ⇒ ⇒ ★★ 所以逐层判。
func gocacheScriptHasInfraSkipDir(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if gocacheScriptInfraSkipDirs[seg] {
			return true
		}
	}
	return false
}

// gocacheScriptDedicatedLines 返回【指向专用目录】的代码行。
//
// ★ ★★ **r1049 审计轮：★ 走【编译型掩码】**（★ 注释与 docstring 不算）★★
// ★ ★★ ⇒ ⇒ ★★ ⇒ 一个「只在变异载荷字符串里」写着 `.build-local/.gocache-` 的
// ★ ★★ ⇒ ⇒ ★★ 文件**不再**算有专用证据 ⇒ ⇒ ★★ ⇒ **那才是真证据** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ 门因此抓到了 `scripts/audit/mutate_r1046_hollowout_gocache_isolation.py`
// ★ ★★ ⇒ ⇒ ★★ ⇒ ★★ 那个「自己拼路径 + 不失败安全 + 靠载荷蒙混过关」的真缺陷。
func gocacheScriptDedicatedLines(rel string, code []string) []string {
	var out []string
	for _, l := range gocacheScriptCompileCode(rel, code) {
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
func gocacheScriptUnconditionalDedicatedLines(rel string, code []string) []string {
	var out []string
	for _, l := range gocacheScriptDedicatedLines(rel, code) {
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
func gocacheScriptNonExportGocacheLines(rel string, code []string) []string {
	var out []string
	for _, l := range gocacheScriptCompileCode(rel, code) {
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
		if !gocacheScriptHasCompile(f.Rel, f.Code) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ── ★★ 作用域清单导出（★ r1049 审计轮加）★★

// gocacheScriptScopeDumpEnv 是【把作用域清单导出到文件】的环境变量名。
//
// ★ ★★ **为什么要有它**：★★ 本门是【静态】判据 ⇒ ⇒ 它只说得出「这个文件**有**那个
// ★★ 形态」⇒ ⇒ **说不出「跑起来真的是那样」** ⇒ ⇒ ★★ ⇒ 而上一轮那版运行期验证器
// ★★ **自己重新枚举脚本族** ⇒ ⇒ ⇒ 两边的【枚举集】可能不同 ⇒ ⇒ ⇒ 「覆盖完整」这句话
// ★★ 就只是【两把尺各自量自己】⇒ ⇒ ⇒ **量具不同源 ⇒ ⇒ 结论不可比**。
// ★★ ⇒ ⇒ 本导出把【本门认定的作用域集】交给运行期验证器 ⇒ ⇒ ⇒ 两边**对着同一份
// ★★ 清单**逐个核对 ⇒ ⇒ ⇒ 「静态覆盖的每一个，都被运行期验过」才成立。
//
// ★ ★★ **默认不写任何文件**（★ 环境变量没设 ⇒ ⇒ 直接返回）★★ ⇒ ⇒ ⇒ 跑整包门时
// ★★ **不会在仓里落下垃圾** ⇒ ⇒ ⇒ 也不会影响「工作区必须干净」那些纪律。
const gocacheScriptScopeDumpEnv = "GOCACHE_GATE_SCOPE_DUMP"

// gocacheScriptDumpScope 把【在作用域内的编译型脚本清单】写到环境变量指向的文件。
//
// ★ ★★ **格式**：★ 一行一个【相对仓根的路径】⇒ ⇒ ⇒ 可直接 `diff`。
func gocacheScriptDumpScope(inScope []gocacheScriptFile) {
	dst := os.Getenv(gocacheScriptScopeDumpEnv)
	if dst == "" {
		return
	}
	var b strings.Builder
	for _, f := range inScope {
		b.WriteString(f.Rel)
		b.WriteString("\n")
	}
	if err := os.WriteFile(dst, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "★ 写作用域清单失败（%s）：%v\n", dst, err)
	}
}

// ── ★★ GOCACHE 赋值点（★ r1049 审计轮重做：★ 支持【下标形态】）★★

// gocacheScriptVarAssignsMasked 找【某个变量的赋值点】（★ 走【编译型掩码】）。
//
// ★ ★★ **为什么要重做（★ 旧的那个看不见 Python 形态）**：★★ 旧的
// ★ ★★ `gocacheScriptVarAssignments` 跑在【普通字符串算数据】的掩码上 ⇒ ⇒ ★★
// ★ ★★ ⇒ `os.environ["GOCACHE"] = …` 里的 `"GOCACHE"` 是**字符串** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ **整条赋值对它是不可见的** ⇒ ⇒ ★★ ⇒ **判据③（不许指向用户目录）只对
// ★ ★★ shell 形态有效** ⇒ ⇒ ★★ ⇒ 一个 Python 脚本写
// ★ ★★ `os.environ["GOCACHE"] = os.path.expanduser("~/.cache/go-build")`
// ★ ★★ ⇒ ⇒ ★★ ⇒ **门照样绿** ⇒ ⇒ ★★ ⇒ **那是假绿** ⇒ ⇒ ★★ ⇒ 本函数用
// ★ ★★ 【编译型掩码】（★ 普通字符串算代码）⇒ ⇒ ★★ ⇒ 下标形态看得见。
//
// ★ ★★ **两种形态都要认**：★★
// ★ 形态甲：★ `GOCACHE = …`（★ shell 的 `export GOCACHE=…` / python 裸变量）；
// ★ 形态乙：★ `["GOCACHE"] = …` / `['GOCACHE'] = …`（★ python 下标形态）。
func gocacheScriptVarAssignsMasked(rel string, code []string, varName string) []gocacheGocacheAssign {
	masked := gocacheScriptCompileMask(rel, code)
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
			// ★ ★★ 形态乙：★ `["GOCACHE"]` / `['GOCACHE']` ⇒ ⇒ 跳过闭合的引号与方括号。
			if k+1 < len(ml.Text) && (ml.Text[k] == '"' || ml.Text[k] == '\'') &&
				ml.Text[k+1] == ']' && k+1 < len(ml.IsCode) && ml.IsCode[k+1] {
				k += 2
			}
			for k < len(ml.Text) && (ml.Text[k] == ' ' || ml.Text[k] == '\t') {
				if k < len(ml.IsCode) && !ml.IsCode[k] {
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
	// ★★ 作用域清单导出（★ 默认不写 ⇒ ⇒ 不给仓留垃圾）★★
	gocacheScriptDumpScope(inScope)

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
	// ★ ★★ **逐字对拍（★ r1049 审计轮加）**：★★ 作用域的**实测个数**必须与登记簿
	// ★ ★★ 里的 `measuredCompileFiles` **完全相等** ⇒ ⇒ ★★ ⇒ 漂了就当场红。
	//
	// ★ ★★ **为什么地板不够**：★★ `minCompileFiles` 是**地板** ⇒ ⇒ ★★ ⇒ 它只挡
	// ★ ★★ 「掉得太多」⇒ ⇒ ★★ ⇒ **挡不住「口径悄悄变了个形状」** ⇒ ⇒ ★★ ⇒ 实测
	// ★ ★★ ★★ 缺陷就是证据：★ 本轮掩码状态机错位 ⇒ ⇒ 作用域从 164 掉到 163 ⇒ ⇒
	// ★ ★★ ⇒ ★★ 地板 100 **照样绿** ⇒ ⇒ ★★ ⇒ ★★ **地板抓不住这一类缺陷** ⇒ ⇒ ⇒
	// ★ ★★ ⇒ 真正抓住它的是【独立量具交叉对拍】★ ⇒ ⇒ ⇒ ⇒ 本条登记值是给
	// ★ ★★ ⇒ **下一次**漂移兜底的（★ 漂了就得有人重新审一遍口径 ★）。
	if reg.MeasuredCompileFiles <= 0 {
		t.Fatalf("★ 登记簿里没有 `measuredCompileFiles`（★ 实测 %d）⇒ ⇒ 【逐字对拍失效】⇒ ⇒ "+
			"★ ⇒ ★★ 作用域漂了没人知道 ⇒ ⇒ ⇒ 补上这个字段", len(inScope))
	}
	if len(inScope) != reg.MeasuredCompileFiles {
		t.Fatalf("★ 扫到 %d 个【在作用域内的编译型脚本】，登记簿记的是 %d 个 ⇒ ⇒ "+
			"★ ⇒ 【作用域漂了】⇒ ⇒ ★★ 本门此刻盖的**不是**登记册上那一套 ⇒ ⇒ ⇒ "+
			"★ ⇒ 要么有人新加了 / 删了编译型脚本，要么**扫描口径变了**（★ ★★ 口径漂了 ⇒ ⇒ "+
			"★ ⇒ ★★ 【假绿】就是这么来的 ★★）⇒ ⇒ ⇒ 重新审一遍，★ 然后把登记簿里的 "+
			"★ ⇒ `measuredCompileFiles` 改成实测值",
			len(inScope), reg.MeasuredCompileFiles)
	}

	var noEvidence, onlySetdefault []string
	for _, f := range inScope {
		dedicated := gocacheScriptDedicatedLines(f.Rel, f.Code)
		if len(dedicated) == 0 {
			noEvidence = append(noEvidence, f.Rel)
			continue
		}
		if len(gocacheScriptUnconditionalDedicatedLines(f.Rel, f.Code)) == 0 {
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
		for _, l := range gocacheScriptCompileCode(f.Rel, f.Code) {
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
		fileHasDedicated := len(gocacheScriptUnconditionalDedicatedLines(f.Rel, f.Code)) > 0
		ok := false
		for _, l := range gocacheScriptNonExportGocacheLines(f.Rel, f.Code) {
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
		if !gocacheScriptHasCompile(f.Rel, f.Code) {
			continue
		}
		for _, a := range gocacheScriptVarAssignments(f.Rel, f.Code, "GOCACHE") {
			if gocacheScriptIsUserGlobalGoCacheValue(a.Rest) {
				offenders = append(offenders, fmt.Sprintf("%s:%d（★ %s）", f.Rel, a.Line, a.Text))
				break
			}
		}
	}
	// ★ ★★ **r1049 审计轮补的一条（★ 补上【Python 下标形态】的盲区）**：★★
	// ★ ★★ 上面那个检测器跑在【普通字符串算数据】的掩码上 ⇒ ⇒ ★★ ⇒ 而
	// ★ ★★ `os.environ["GOCACHE"] = …` 里的 `"GOCACHE"` **是字符串** ⇒ ⇒ ★★ ⇒
	// ★ ★★ ⇒ **整条赋值对它不可见** ⇒ ⇒ ★★ ⇒ **判据③ 只对 shell 形态有效**
	// ★ ★★ ⇒ ⇒ ★★ ⇒ 一个 Python 脚本写
	// ★ ★★ `os.environ["GOCACHE"] = os.path.expanduser("~/.cache/go-build")`
	// ★ ★★ ⇒ ⇒ ★★ ⇒ **门照样绿** ⇒ ⇒ ★★ ⇒ **那是假绿** ⇒ ⇒ ★★ ⇒ ★★ ⇒
	// ★ ★★ ⇒ 这里补一条【窄】判据：★ **下标形态** ＋ ★ **取值**指向用户目录。
	// ★ ★★ ⇒ ⇒ ★★ ⇒ **窄**是为了不误伤【变异台的载荷字符串】（★ 载荷是**数据**，
	// ★ ★★ ⇒ ⇒ 按仓内既有约定「字符串里的 GOCACHE= 不是赋值」⇒ ⇒ ★★ ⇒ 不算）。
	for _, f := range files {
		if !gocacheScriptHasCompile(f.Rel, f.Code) {
			continue
		}
		for i, l := range f.Code {
			if gocacheScriptSubscriptGocacheRE.MatchString(l) {
				offenders = append(offenders, fmt.Sprintf("%s:%d（★ %s）",
					f.Rel, i+1, strings.TrimSpace(l)))
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
	// ★ ★★ **r1049 审计轮补的（★ 判据④ 的同款盲区）**：★★ Python 下标形态
	// ★ ★★ `os.environ["GOMODCACHE"] = "…/.build-local/…"` ⇒ ⇒ ★★ ⇒ 主检测器
	// ★ ★★ 看不见（★ `"GOMODCACHE"` 是字符串）⇒ ⇒ ★★ ⇒ 这里补一条**窄**判据。
	for _, f := range files {
		for i, l := range f.Code {
			if gocacheScriptSubscriptGomodcacheRE.MatchString(l) {
				offenders = append(offenders, fmt.Sprintf("%s:%d（★ %s）",
					f.Rel, i+1, strings.TrimSpace(l)))
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
		if !gocacheScriptHasCompile(f.Rel, f.Code) {
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

// ── ★★ 判据⑥（★ r1049 审计轮新增）：不许用「只算路径」的入口设 GOCACHE ──

// TestScriptFamilyNeverSetsGoCacheFromDirFor —— ★★ 判据⑥：
// ★ 凡是**把 GOCACHE 设成 `gocache.dir_for(...)` 的结果**的脚本 ⇒ ⇒ 必须红。
//
// ★ ★★ **为什么这一条独立于判据①**：★★ 判据① 是**文件级**的 ⇒ ⇒ ★★ 一个文件里
// ★ ★★ 「既有 `apply_to_environ`（★ 合格）又有 `dir_for`（★ 不合格）」⇒ ⇒ ★★
// ★ ★★ 判据① 仍然绿 ⇒ ⇒ ★★ ⇒ ★★ ⇒ **不合格的那一处会被漏掉** ⇒ ⇒ ★★ ⇒ ★★ ⇒
// ★ ★★ ⇒ 所以要**单独**盯住那一处（★ 与判据③ 同款思路）。
//
// ★ ★★ **为什么它是真的缺陷（★ 不是洁癖）**：★★ `dir_for()` **只算路径**：★★
// ★ ★★ 它**不建目录**、★★ **不查可写** ⇒ ⇒ ★★ 而 go 拿到一个**不可用**的
// ★ ★★ GOCACHE 会直接报 `failed to initialize build cache` ⇒ ⇒ ★★ ⇒ **脚本坏掉**
// ★ ★★ ⇒ ⇒ ★★ ⇒ 违反 r1048 第一原则「**宁可漏隔离，也不许把脚本弄坏**」。
//
// ★ ★★ **作用域**：★ **全部**脚本（★ 不只是编译型）⇒ ⇒ ★★ 因为「把 GOCACHE 指到
// ★ ★★ 一个建不出来的目录」这件事本身就有害 ⇒ ⇒ ★★ ⇒ 与它是否编译型无关。
func TestScriptFamilyNeverSetsGoCacheFromDirFor(t *testing.T) {
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}

	var offenders []string
	var sawGoCacheAssign int
	for _, f := range files {
		// ★ ★★ **按【原始行】扫**（★ `f.Code` 现在就是原始行 ⇒ ⇒ 行号一一对应）★★
		codeText := gocacheScriptFullCodeText(f.Rel, f.Code)
		for i, cl := range codeText {
			if i >= len(f.Code) {
				break
			}
			// ★ ★★ **搜索面完整性自检**：★ 一个 GOCACHE 赋值都没扫到 ⇒ ⇒
			// ★ ★★ ⇒ 本判据的**正则**可能已经失效 ⇒ ⇒ ★★ ⇒ 当场红（★ 不许假绿）。
			if strings.Contains(cl, "GOCACHE") {
				sawGoCacheAssign++
			}
			// ★ ★★ **判别式（★ 本轮被 B00 逼出来的）**：★★
			// ★ ★★ `gocache.dir_for(` **这个调用本身**必须在【代码】里 ⇒ ⇒ ★★ ⇒
			// ★ ★★ ⇒ ★★ 否则【变异台的载荷字符串】会被当成真赋值 ⇒ ⇒ ★★ ⇒ ★★ ⇒
			// ★ ★★ ⇒ ★★ ★★ （★ 事实：★ 本轮 B00 惰性筛查就抓到了这个 —— 事实是
			// ★ ★★ ★★ ★★ 本台自己的 M14 载荷里那句 `os.environ["GOCACHE"] =
			// ★ ★★ ★★ ★★ gocache.dir_for(...)` ⇒ ⇒ ★★ ⇒ 把基线搞红了 ⇒ ⇒ ★★ ⇒
			// ★ ★★ ★★ ★★ ⇒ ★★ ★★ ⇒ **载荷是数据，不是赋值** ⇒ ⇒ ★★ ⇒ ★★ ⇒ ★★）。
			if !strings.Contains(cl, "gocache.dir_for(") {
				continue
			}
			if gocacheScriptDirForGocacheRE.MatchString(f.Code[i]) {
				offenders = append(offenders, fmt.Sprintf("%s:%d（★ %s）",
					f.Rel, i+1, strings.TrimSpace(f.Code[i])))
				break
			}
		}
	}

	if sawGoCacheAssign == 0 {
		t.Fatalf("★ 一个【带 GOCACHE 的行】都没扫到 ⇒ ⇒ 【本判据的搜索面不完整】⇒ ⇒ " +
			"★ 正则可能已失效 ⇒ ⇒ 本门是瞎的 ⇒ ⇒ 重新审一遍")
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("★ %d 处把 **GOCACHE** 设成了 `gocache.dir_for(...)` 的结果（★ ⇒ ⇒ "+
			"★ **只算路径**：★ **不建目录**、★ **不查可写** ⇒ ⇒ ★★ ⇒ 而 go 拿到一个"+
			"**不可用**的 GOCACHE 会直接报 `failed to initialize build cache` ⇒ ⇒ ★★ ⇒ "+
			"**脚本直接坏掉** ⇒ ⇒ ★★ ⇒ 违反 r1048 第一原则「宁可漏隔离，也不许把脚本弄坏」）：\n  %s\n"+
			"★ ⇒ ⇒ 修法：★ Python 用 `gocache.apply_to_environ(\"mutate\")`"+
			"（★ 当前进程）★ 或 `gocache.env(\"mutate\")`（★ 子进程 env）★ ⇒ ⇒ "+
			"★ 两者都先 `ensure()` 建目录并查可写 ⇒ ⇒ 建不出来就**不设** ⇒ ⇒ 脚本照常跑",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// ── ★★ 门内自证：判据⑥「`dir_for` 不许当 GOCACHE 值」（★ r1049 审计轮新增）★★

// TestScriptFamilyDirForJudgeDistinguishesPayloadFromCode —— ★★ 门内自证：
// ★ 判据⑥ 必须分得清「**代码里**的 `dir_for` 调用」与「**载荷字符串**里提到它」。
//
// ★ ★★ **为什么必须自证（★ 本轮被 B00 惰性筛查当场抓到）**：★★ 第一版判据⑥
// ★ ★★ 跑在【普通字符串算代码】的视图上 ⇒ ⇒ ★★ ⇒ ★★ ⇒ 结果**本台自己的 M14
// ★ ★★ 载荷字符串**（★ `M14_REPL` 里那句 `os.environ["GOCACHE"] =
// ★ ★★ gocache.dir_for(...)`）★ ⇒ ⇒ ★★ ⇒ ★★ ⇒ **被当成了真赋值** ⇒ ⇒ ★★ ⇒ ★★ ⇒
// ★ ★★ ⇒ ★★ **基线就红** ⇒ ⇒ ★★ ⇒ ★★ ⇒ ⇒ ★★ ⇒ **B00 惰性筛查救了这一轮**。
func TestScriptFamilyDirForJudgeDistinguishesPayloadFromCode(t *testing.T) {
	// ── 正向：★ 代码里的真调用必须被抓 ──
	codeMustCatch := []string{
		`os.environ["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")`,
		`env["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")`,
	}
	for _, s := range codeMustCatch {
		rel := "scripts/x.py"
		raw := strings.Split(s, "\n")
		text := gocacheScriptFullCodeText(rel, raw)
		caught := false
		for i, cl := range text {
			if strings.Contains(cl, "gocache.dir_for(") &&
				gocacheScriptDirForGocacheRE.MatchString(raw[i]) {
				caught = true
				break
			}
		}
		if !caught {
			t.Fatalf("★ ★★ 这条形态必须被判【用 dir_for 设了 GOCACHE】★★ ⇒ ⇒ 但没抓到 ⇒ ⇒ "+
				"★ **判据⑥ 是瞎的** ⇒ ⇒ 【假绿】：\n  %s", s)
		}
	}

	// ── 反向：★ 载荷字符串（★ 数据）**不许**被抓 ──
	payloadMustNotCatch := []string{
		`M14_REPL = 'os.environ["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")'`,
		`# os.environ["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")`,
		// ★ ★★ docstring 那一条必须**写成【同一个字符串里的三行】** ★ ⇒ ⇒ ★★
		// ★ ★★ 否则拆行后就不是 docstring 了 ⇒ ⇒ ★★ ⇒ ★★ ⇒ （★ 本轮第一版就
		// ★ ★★ 踩了这个 ⇒ ⇒ ★★ ⇒ ★★ ⇒ **是测试写错，不是掩码错** ⇒ ⇒ ★★ ⇒ ★★ ⇒
		// ★ ★★ ⇒ ★★ ⇒ ★★ ★★ 「拆行」会改变语言语义 ⇒ ⇒ ★★ ⇒ ★★ ⇒ ★★ ⇒ 写自证
		// ★ ★★ ⇒ ★★ ⇒ ★★ ⇒ 样例时必须**整体给**）。
		"\"\"\"\nos.environ[\"GOCACHE\"] = gocache.dir_for(gocache.repo_root(), \"mutate\")\n\"\"\"",
	}
	for _, s := range payloadMustNotCatch {
		rel := "scripts/x.py"
		raw := strings.Split(s, "\n")
		text := gocacheScriptFullCodeText(rel, raw)
		for i, cl := range text {
			if strings.Contains(cl, "gocache.dir_for(") &&
				gocacheScriptDirForGocacheRE.MatchString(raw[i]) {
				t.Fatalf("★ ★★ 这条形态**不许**被抓（★ 它是【载荷 / 注释 / docstring】★ ⇒ ⇒ "+
					"★ ⇒ 数据，不是赋值 ⇒ ⇒ ⇒ 抓它 ⇒ ⇒ ⇒ **误伤【治病的工具自己】**）：\n  %s", s)
			}
		}
	}
}

// ── ★★ 门内自证：用户目录覆盖的两种形态（★ r1049 审计轮新增）★★

// TestScriptFamilyUserGlobalOverrideDetectsBothForms —— ★★ 门内自证：
// ★ 「把 GOCACHE 指向仓外用户目录」这条判据 ⇒ ⇒ 必须**两种形态都抓得到**。
//
// ★ ★★ **为什么必须自证**：★★ 判据③ 的主检测器跑在【普通字符串算数据】的
// ★ ★★ 掩码上 ⇒ ⇒ ★★ ⇒ **看不见** Python 的 `os.environ["GOCACHE"] = …` 形态
// ★ ★★ ⇒ ⇒ ★★ ⇒ 补了一条**窄**正则 ⇒ ⇒ ★★ ⇒ **窄正则自己也要被量** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ 否则「窄」可能窄到**什么都抓不到** ⇒ ⇒ ★★ ⇒ 而它绿着。
func TestScriptFamilyUserGlobalOverrideDetectsBothForms(t *testing.T) {
	// ── 正向：★ Python 下标形态（★ 主检测器看不见的那一种）必须被抓 ──
	subscriptMustCatch := []string{
		`os.environ["GOCACHE"] = os.path.expanduser("~/.cache/go-build")`,
		`env["GOCACHE"] = "/Users/someone/.cache/go-build"`,
		`os.environ["GOCACHE"] = os.path.join(HOME, "go-build")`,
	}
	for _, s := range subscriptMustCatch {
		if !gocacheScriptSubscriptGocacheRE.MatchString(s) {
			t.Fatalf("★ ★★ 这条形态必须被判【指向仓外用户目录】★★ ⇒ ⇒ 但窄判据没抓到 ⇒ ⇒ "+
				"★ **判据③ 对 Python 形态就是瞎的** ⇒ ⇒ 【假绿】：\n  %s", s)
		}
	}

	// ── 正向：★ shell 形态（★ 主检测器负责的那一种）必须被抓 ──
	shellMustCatch := []string{
		`export GOCACHE="$HOME/.cache/go-build"`,
		`GOCACHE="$HOME/.cache/go-build"`,
	}
	for _, s := range shellMustCatch {
		rel := "scripts/x.sh"
		code := []string{s}
		caught := false
		for _, a := range gocacheScriptVarAssignments(rel, code, "GOCACHE") {
			if gocacheScriptIsUserGlobalGoCacheValue(a.Rest) {
				caught = true
				break
			}
		}
		if !caught {
			t.Fatalf("★ ★★ 这条形态必须被判【指向仓外用户目录】★★ ⇒ ⇒ 但主检测器没抓到 ⇒ ⇒ "+
				"★ 判据③ 退化了 ⇒ ⇒ 【假绿】：\n  %s", s)
		}
	}

	// ── 反向：★ 变异台的【载荷字符串】是数据 ⇒ ⇒ 不许被当成赋值 ──
	payloadMustNotCatch := []string{
		`M09_REPL = '  export GOCACHE="$HOME/.cache/go-build"  # MUTATION M09'`,
		`M12_REPL = 'GOMODCACHE="${GOMODCACHE:-$HOME/.build-local/gomodcache}"'`,
		`env = gocache.env("mutate")`,
		`gocache.apply_to_environ("mutate")`,
	}
	for _, s := range payloadMustNotCatch {
		if gocacheScriptSubscriptGocacheRE.MatchString(s) {
			t.Fatalf("★ ★★ 这条形态**不许**被抓 ⇒ ⇒ 它是【载荷字符串】（★ 数据 ⇒ ⇒ "+
				"★ 按仓内约定「字符串里的 GOCACHE= 不是赋值」）⇒ ⇒ ⇒ 抓它 ⇒ ⇒ ⇒ "+
				"★ **误伤【治病的工具自己】**：\n  %s", s)
		}
		rel := "scripts/x.py"
		for _, a := range gocacheScriptVarAssignments(rel, []string{s}, "GOCACHE") {
			if gocacheScriptIsUserGlobalGoCacheValue(a.Rest) {
				t.Fatalf("★ ★★ 这条形态**不许**被抓（★ 载荷是数据）：\n  %s", s)
			}
		}
	}
}

// ── ★★ 门内自证：编译型判定的作用域口径（★ r1049 审计轮新增）★★

// TestScriptFamilyCompileScopeIgnoresDocstrings —— ★★ 门内自证：
// ★ 编译型判定**必须**分得清「代码里的 go 调用」与「注释 / docstring 里的 go 字样」。
//
// ★ ★★ **为什么这条自证不可省**：★★ ★★ 「把 docstring 里的 `go test ./...` 也算成
// ★ ★★ 编译调用」⇒ ⇒ ★★ 作用域虚高（★ 实测 3 个文件）⇒ ⇒ ★★ ⇒ 那些文件被**要求**
// ★ ★★ 提供专用隔离 ⇒ ⇒ ★★ ⇒ **假红** ⇒ ⇒ ★★ ⇒ ⇒ 而「把真正的调用也抹掉」⇒ ⇒
// ★ ★★ ⇒ **漏判** ⇒ ⇒ ★★ ⇒ **更糟** ⇒ ⇒ ★★ ⇒ ⇒ 两个方向都要钉住。
func TestScriptFamilyCompileScopeIgnoresDocstrings(t *testing.T) {
	// ── 正向：★ 真调用必须被认成【编译型】──
	positive := []struct {
		name string
		rel  string
		code []string
	}{
		{"shell 裸词形态", "scripts/x.sh", []string{"go test ./admin/ -count=1"}},
		{"python 列表形态", "scripts/x.py", []string{`cmd = ["go", "test", "./admin/"]`}},
		{"python 字符串里的裸词形态", "scripts/x.py", []string{`run("go vet ./scripts/ursmcheck/")`}},
		{"shell 行尾注释不算", "scripts/x.sh", []string{"go build ./cmd/...  # 不是测试"}},
	}
	for _, c := range positive {
		if !gocacheScriptHasCompile(c.rel, c.code) {
			t.Fatalf("★ ★★ %s：这形态必须判【编译型】★★ ⇒ ⇒ 但它没被认 ⇒ ⇒ "+
				"★ 本门的编译型判定退化了 ⇒ ⇒ **真调用被漏掉了**（★ 比虚高危险得多）", c.name)
		}
	}

	// ── 反向：★ 注释 / docstring 里的 go 字样**不算**编译调用 ──
	negative := []struct {
		name string
		rel  string
		code []string
	}{
		{"python 整行注释", "scripts/x.py", []string{"# go test ./admin/"}},
		{"shell 整行注释", "scripts/x.sh", []string{"# go test ./admin/"}},
		{"python 模块 docstring", "scripts/x.py", []string{`"""`, `★ 实测：★ go test ./... **编译阶段**就红`, `"""`}},
		{"python docstring 里的列表形态", "scripts/x.py", []string{`"""`, `["go", "test"]`, `"""`}},
	}
	for _, c := range negative {
		if gocacheScriptHasCompile(c.rel, c.code) {
			t.Fatalf("★ ★★ %s：这形态必须判【非编译型】★★（★ 它在注释 / docstring 里 ⇒ ⇒ "+
				"★ ⇒ ★★ 算进去会让作用域虚高 ⇒ ⇒ 那些文件被无谓地要求隔离 ⇒ ⇒ 假红）⇒ ⇒ "+
				"★ 但它被判成了编译型", c.name)
		}
	}

	// ── 仓库级回归：★ 那 3 个「只在 docstring 里提到 go 命令」的文件必须**不在作用域内** ──
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}
	inScope := map[string]bool{}
	for _, f := range gocacheScriptInScope(files) {
		inScope[f.Rel] = true
	}
	mustBeOut := []string{
		"scripts/lib/gocache.py",
		"scripts/perf/bench_compare.py",
		"scripts/audit/mutate_r1047_gocache_isolation_rollout.py",
	}
	for _, rel := range mustBeOut {
		if inScope[rel] {
			t.Fatalf("★ ★★ %s 必须**不在**编译型作用域内 ★★（★ 它只在 docstring 里提到 go "+
				"★ 命令 ⇒ ⇒ ★★ ⇒ 进作用域 ⇒ ⇒ 就要被无谓地要求专用隔离 ⇒ ⇒ 假红）★ ⇒ ⇒ "+
				"★ 但它在里面", rel)
		}
	}

	// ★ ★★ **搜索面完整性自检**：★ 在作用域内的脚本一个都没有 ⇒ ⇒ 本门是瞎的。
	if len(inScope) == 0 {
		t.Fatalf("★ 一个【在作用域内、且调编译型 go 命令】的脚本都没扫到 ⇒ ⇒ 【搜索面不完整】")
	}
}

// ── ★★ 门内自证：掩码的「状态机不许错位」（★ r1049 审计轮新增）★★

// TestScriptFamilyCompileScopeSurvivesTripleQuoteClosingOnCommentLine —— ★★
// ★ 编译型判定**必须**扛得住「三引号块的闭合定界符落在一条以 `#` 开头的行上」。
//
// ★ ★★ **这是 r1049 审计轮实测踩到的真缺陷（★ 而且是【假绿】）**：★★
// ★ ★★ 旧实现是【先剥整行注释 ⇒ ⇒ 再打掩码】⇒ ⇒ ★★ ⇒ 而 Python 里三引号块
// ★ ★★ **内部**的 `#` **不是注释** ⇒ ⇒ ★★ ⇒ 那条「以 `#` 开头」的行里装的是
// ★ ★★ **闭合定界符** ⇒ ⇒ ★★ ⇒ 「剥整行注释」把它**删掉** ⇒ ⇒ ★★ ⇒ 闭合符消失
// ★ ★★ ⇒ ⇒ 掩码状态机从此错位 ⇒ ⇒ ★★ ⇒ **该文件后半段全被当成数据** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ 它调的那些 go 命令**门看不见** ⇒ ⇒ ★★ ⇒ **作用域少算** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ **那个文件整个逃出了门的管辖** ⇒ ⇒ ★★ ⇒ **假绿**。
// ★ ★★ ⇒ ⇒ 实测受害者：`scripts/mutate-r164-freshness-gate.py`（★ 它的
// ★ ★★ ⇒ `["go", "test", "./scripts/ursmcheck/", …]` 在旧口径下**根本不存在**）。
// ★ ★★ ⇒ ⇒ ★★ 它是本轮【独立量具（★ Python `tokenize` 口径）交叉对拍】抓到的
// ★ ★★ ⇒ ⇒ ★★ 不是门自己抓到的 ⇒ ⇒ ★★ ⇒ **门在自己的盲区里是绿的**。
func TestScriptFamilyCompileScopeSurvivesTripleQuoteClosingOnCommentLine(t *testing.T) {
	// ── 正向：★ 闭合定界符落在「以 `#` 开头」的行上 ⇒ ⇒ ★★ 后面那个真调用
	// ── ⇒ ⇒ **必须**仍然被认成编译型 ⇒ ⇒ ★★ 否则整个文件逃出管辖。
	code := []string{
		`X = """def die(msg: str, code: int = 3):`,
		`    # ★ 对照变异：★ 只加注释 ⇒ ★ 行为不变 ⇒ ★ 门必须仍绿"""`,
		`cmd = ["go", "test", "./scripts/ursmcheck/"]`,
	}
	if !gocacheScriptHasCompile("scripts/x.py", code) {
		t.Fatalf("★ ★★ 三引号块的闭合定界符落在注释行上时 ⇒ ⇒ 后面那个 `[\"go\", \"test\"]` " +
			"★ **必须**仍被判【编译型】★★ ⇒ ⇒ 但它没被认 ⇒ ⇒ ⇒ ★★ **掩码状态机错位了** " +
			"★ ⇒ ⇒ ⇒ 那个文件整个逃出本门管辖 ⇒ ⇒ ⇒ **假绿**")
	}

	// ── 反向：★ 整段都在三引号块里 ⇒ ⇒ ★★ 仍然**不算**编译型（★ 别矫枉过正）。
	only := []string{
		`X = """def die(msg: str, code: int = 3):`,
		`    # ★ 对照变异：★ 只加注释 ⇒ ★ 行为不变 ⇒ ★ 门必须仍绿"""`,
		`cmd = ["list", "of", "things"]`,
	}
	if gocacheScriptHasCompile("scripts/x.py", only) {
		t.Fatalf("★ ★★ 这段里没有编译型 go 调用 ⇒ ⇒ 必须判【非编译型】★★ ⇒ ⇒ 但它被判成了编译型")
	}

	// ── ★★ 仓库级回归：★ 那个受害者文件必须**回到**作用域内 ★─
	root := hollowRepoRoot(t)
	reg := gocacheLoadExclusionRegistry(t, root)
	files, err := gocacheScriptScan(root, gocacheExcludedPrefixes(reg))
	if err != nil {
		t.Fatalf("扫描脚本族失败：%v", err)
	}
	inScope := map[string]bool{}
	for _, f := range gocacheScriptInScope(files) {
		inScope[f.Rel] = true
	}
	const victim = "scripts/mutate-r164-freshness-gate.py"
	if !inScope[victim] {
		t.Fatalf("★ ★★ %s 必须在编译型作用域内 ★★（★ 它的 `[\"go\", \"test\", …]` 是**真调用** "+
			"★ ⇒ ⇒ ⇒ 它必须被本门管辖 ⇒ ⇒ ⇒ 它不在 ⇒ ⇒ ⇒ **掩码状态机又错位了** ★★ ⇒ ⇒ ⇒ "+
			"★ ⇒ ⇒ ⇒ **假绿**（★ r1049 审计轮实测：★ 旧口径下它整个逃出了管辖））", victim)
	}
}

// TestScriptFamilyFullMaskTripleSingleQuoteDoesNotCloseEarly —— ★★
// ★ 【完整掩码】（★ 普通字符串 = 数据 ⇒ 服务于「这个 `dir_for(` 是不是真代码」）
// ★ **不许**在 `”'` 块内部遇到 `”` 时提前闭合。
//
// ★ ★★ **为什么**：★★ 旧实现把 `”'` 的闭合定界符写成了**两个**引号（★ `”`）★ ⇒
// ★ ★★ ⇒ 块内部只要出现 `”`（★ 例：★ 空串 `”` ★ 或 `don”t` ★）⇒ ⇒ ★★ ⇒ 提前闭合
// ★ ★★ ⇒ ⇒ 块后面的**真代码**被当成数据 ⇒ ⇒ ★★ ⇒ 判据⑥ 会漏掉真的 `dir_for(`。
func TestScriptFamilyFullMaskTripleSingleQuoteDoesNotCloseEarly(t *testing.T) {
	code := []string{
		`X = '''`,
		`payload = ''`,
		`'''`,
		`os.environ["GOCACHE"] = gocache.dir_for(root, "mutate")`,
	}
	full := gocacheScriptFullCodeText("scripts/x.py", code)
	joined := ""
	for _, l := range full {
		joined += l + "\n"
	}
	if !strings.Contains(joined, "dir_for") {
		t.Fatalf("★ ★★ `'''` 块内的 `''` **不许**让块提前闭合 ★★ ⇒ ⇒ ⇒ 块后面那行真代码 "+
			"★ ⇒ ⇒ ⇒ 必须仍看得见 `dir_for(` ⇒ ⇒ ⇒ 但它被当成数据抹掉了 ⇒ ⇒ ⇒ ⇒ "+
			"★ ⇒ ⇒ ⇒ **判据⑥ 会漏判**（★ 实测掩码输出：\n%s）", joined)
	}
	// ★ ★★ 反向：★ `'''` 块**内部**的 `dir_for(` 必须仍被当成数据。
	inside := []string{
		`X = '''`,
		`os.environ["GOCACHE"] = gocache.dir_for(root, "mutate")`,
		`'''`,
	}
	full2 := gocacheScriptFullCodeText("scripts/x.py", inside)
	joined2 := ""
	for _, l := range full2 {
		joined2 += l + "\n"
	}
	if strings.Contains(joined2, "dir_for") {
		t.Fatalf("★ ★★ `'''` 块内部的 `dir_for(` 必须仍被当成数据 ★★（★ 载荷 ⇒ ⇒ 数据）⇒ ⇒ ⇒ "+
			"★ ⇒ ⇒ ⇒ 但它没被抹掉 ⇒ ⇒ ⇒ ⇒ （★ 实测掩码输出：\n%s）", joined2)
	}
}
