# 远端 `origin/main` 的 CI 验证记录（2026-11-11）

> 目标：给远端 `origin/main` 建立一条**可追溯**的 CI 验证记录，明确「提交后的构建、回归、覆盖率门禁」是否通过、
> 以及**用什么方法、在什么树上、得到什么读数**得出的结论。
> 本文件是**记录**，不是「CI 平台跑出来的绿灯截图」——原因见 §二（诚实披露）。

---

## 〇、结论摘要

| 项 | 结论 | 观测值（方法见 §一、§三） |
|---|---|---|
| 构建 `go build ./...` | ✅ 通过 | 退出码 0；干净树冷编译 37s |
| 回归（IR 包单测 + 覆盖率） | ✅ 通过 | 退出码 0；`internal/ir` 2s |
| **覆盖率门禁（≥ 75%）** | ✅ 通过 | **实测 82.4%**（门限 75%，余量 7.4 个点） |
| 回归（跨模块 12 包族） | ✅ 通过 | 退出码 0；96s |
| 变异门（3 harness / 13 臂） | ✅ 全被抓住 | 5/5 + 4/4 + 4/4 |
| 真实会话回放 | ✅ 通过 | 退出码 0；1s |
| 并发安全（`-race`） | ✅ 通过 | 退出码 0；9–14s |

**验证对象**：`origin/main` @ `48ea8b926522a2ded89fa7004e20eefb1a1d0193`
（= 本地 `main` 合并上游 `f16f9ab9f` 后的合并提交；本文件自身作为其后的**纯文档提交**入库）。

**两条原始 transcript 一并入库**（不是截图，是可复核的逐行输出）：
- `docs/audit/evidence/2026-11-11-origin-main-ci-verify.txt`（远端原始代码态 `5a4f3e71f`，842 行）
- `docs/audit/evidence/2026-11-11-origin-main-ci-verify-final.txt`（最终合并树 `48ea8b926`）

---

## 一、验证对象与方法（可追溯性）

| 维度 | 取值 | 为什么这么取 |
|---|---|---|
| 远端 | `codeup.aliyun.com:kaixuan/official-deploy/llm-gateway-go` | 本仓唯一远端 |
| 验证树 | **独立 worktree**，detached 在目标提交上 | ★ 在主工作树验证**不可追溯**：主树混着他人在途未提交产物（`tests/`、`docs/testing/`、`scripts/testing/` 等），构建面与推送内容不一致 |
| 对照树位置 | 与主树**同父目录**的兄弟目录 | ★ `scripts/deploy-lib` 是**仓内相对符号链接**（`../../../deploy-lib`）⇒ 建在仓内子目录（如 `.worktrees/`）会让它解析到**另一个路径** ⇒ 部署类门以**另一个错误**失败 ⇒ 读数同为 exit=1 ⇒ **差点把环境红误判成真回归**（本仓 `AGENTS.md:509` 已记同一坑） |
| 工作树洁净性 | 每个 step 前核对 `HEAD == 目标 SHA` 且 `git status --porcelain` 为空 | ★ 门必须跑在**确定**的树上 |
| 执行器 | `scripts/audit/ci_mirror_runner.sh`（本轮新增，随本记录入库） | 逐字复刻 `.github/workflows/ir-protocol-conversion-ci.yml` 的 step 序列；某 step 非零 ⇒ 后续标记 SKIPPED（与 GitHub Actions 语义一致） |
| 宿主 / 工具链 | darwin arm64 / `go1.27.2` | ★ 与工作流声明的 `go-version: 1.23` **不一致** ⇒ 见 §八诚实边界 |

---

## 二、★★ 关键披露：本仓远端**没有** GitHub Actions

这一条必须写在最前面，否则整份记录会被误读。

- 本仓远端是 **`codeup.aliyun.com`（阿里云云效）**，**不是 GitHub**。
- ⇒ **`.github/workflows/` 不在本仓的执行路径上** ⇒ 仓内 10 份 GitHub Actions 工作流在远端**一次都不会跑**。
  （本仓自己的 `.workflow/main-verify.yml` 第 64–70 行、`scripts/ursmcheck/ci_carrier_truth_test.go` 的 C3 判据，都白纸黑字写了这件事。）
- ⇒ **真正在跑的载体**是 `.workflow/main-verify.yml`（codeup Flow），其 `audit-guards` step 执行 `make guards-fast`。
- ⇒ ★★ **因此本记录不是「CI 平台跑出来的绿灯」**，而是：
  **在干净 worktree 里，按工作流定义的 step 逐字镜像执行的等价结果**。
  ★ 这是本仓唯一的诚实选项——把「本地镜像执行」说成「CI 跑过了」就是造假。

### ★★ 本轮因此撞出并修复了**我自己上一轮**的惰性接线

| | 上一轮（`c91c0a57a`） | 本轮（`be937550e`） |
|---|---|---|
| 三个变异 harness 的调用方 | 只有 `.github/workflows/ir-protocol-conversion-ci.yml` | **`Makefile` 的 `guards-ir-conversion`**（挂在 `guards` 与 `guards-fast`）＋ `verify.sh` |
| 覆盖率门禁的调用方 | 同上 | 同上 |
| 在远端 CI 上是否执行 | ❌ **一次都没有** | ✅ 由 `make guards-fast` 的非注释行执行到 |

⇒ ★★ 按仓内纪律「**门没有调用方 = 声明不是门**」，上一轮那三个 harness **此前不是门，是文件**——
而且**没人会发现**，因为没人看那个目录。
⇒ ★ 修法与验证见 §五、§六。

---

## 三、逐 step 结果（方法 + 观测值 + 退出码）

执行器逐字复刻 `.github/workflows/ir-protocol-conversion-ci.yml` 的 step 序列，**外加**一条该工作流未覆盖的跨模块回归。

### Run A —— 远端原始代码态 `5a4f3e71f`（worktree 在仓内子目录 `.worktrees/ci-verify`）

| # | step | 命令 | 退出码 | 耗时 |
|---|---|---|---|---|
| 1 | Checkout（核对 HEAD == 目标 SHA 且工作树干净） | — | 0 | 0s |
| 2 | Setup Go（工作流声明 1.23 / 本机实测 1.27.2） | — | — | — |
| 3 | go build | `go build ./...` | **0** | 37s |
| 4 | mutation harness — client error fix | `bash scripts/audit/ir-client-error-fix-mutation.sh` | **0**（5/5 抓住） | 8s |
| 5 | mutation harness — gemini only loss | `bash scripts/audit/ir-gemini-only-loss-mutation.sh` | **0**（4/4 抓住） | 8s |
| 6 | mutation harness — coerce numeric | `bash scripts/audit/ir-coerce-numeric-mutation.sh` | **0**（4/4 抓住） | 8s |
| 7 | ir package tests (with coverage) | `go test ./internal/ir/... -coverprofile -covermode=atomic` | **0** | 2s |
| 8 | coverage gate（ir ≥ 75%） | `awk` 阈值判定 | **0** | 0s |
| 9 | production session replay | `go test ./internal/ir -run TestProductionReplay -v` | **0** | 1s |
| 10 | concurrency (race detector) | `go test -race ./internal/ir -run TestConcurrent` | **0** | 14s |
| 11 | 跨模块回归（★ 本工作流未含，故追加） | `go test ./internal/ir/... ./internal/protocolformat/... ./internal/paramreg/... ./domains/transformation/... ./domains/streaming/... ./cmd/gateway/...` | **0** | 96s |
| — | 工作树还原复核 | `git status --porcelain` | **0 改动** | — |

**Run A 原始输出**：`docs/audit/evidence/2026-11-11-origin-main-ci-verify.txt`

### Run B —— 最终合并树 `48ea8b926`（worktree 为**同父目录兄弟目录**，symlink 解析与主树一致）

**原始输出**：`docs/audit/evidence/2026-11-11-origin-main-ci-verify-final.txt`
（step 序列与 Run A 完全一致；每一步的退出码与耗时见该文件内 `>>> STEP` / `<<< EXIT` 行。）

⇒ ★ 两跑的 step 结论一致 ⇒ 合并上游 8 个提交**没有改变** IR 转换层的任何门禁结论。

---

## 四、覆盖率门禁读数

| 项 | 读数 |
|---|---|
| 指标 | `internal/ir` 语句覆盖率（`go tool cover -func`，`atomic` 模式） |
| 门限 | **75%**（登记在案：留约 7 个点回落余量，又紧到能抓住「整块代码被删/被绕过」） |
| 实测 | **82.4%** |
| 判定 | ✅ 通过（余量 7.4 个点） |

---

## 五、★★ 本轮发现并修复的两处真缺陷

### 缺陷 ①（★★★ 载体层）：CI 接线接到了**不会执行的载体**上

- **现象**：三个变异 harness + 覆盖率门禁的唯一调用方是 `.github/workflows/ir-protocol-conversion-ci.yml`。
- **根因**：本仓远端是 codeup ⇒ `.github/workflows/` 不在执行路径上（仓内 `AGENTS.md:509` / `.workflow/main-verify.yml:64-70` / `ci_carrier_truth_test.go` C3 都已写明）。
- **后果**：门在 CI 上是**空转**的——本地绿、CI 也绿，**但 CI 从没跑过它**。
- **修法**：新增 `Makefile` 的 `guards-ir-conversion` target，挂进 `guards` 与 `guards-fast`
  （⇒ codeup Flow 的 `make guards-fast` 非注释行会执行到）；`verify.sh`（官方门）也直接调三个 harness。
- **顺序**：本 target 挂在两个 target 的**最后一个**前置 ⇒ 变异 harness 中途失败时 make 会中止（前置失败 ⇒ 配方不执行）⇒
  **绝不会带着「被变异过的 `internal/ir`」去跑后续 `go test $(GUARD_PACKAGES)`**。

### 缺陷 ②（★★ 可移植性）：`shasum` 在 Linux 容器里未必存在

- **现象**：三个 harness 都用 `shasum -a 256`——这是 macOS/Perl 系的命令。
- **风险**：CI runner 是 `public/cn-eastwood2_4c8g_linux_x86_64`（Linux 容器）⇒ 精简镜像上**可能没有** `shasum`
  ⇒ harness 会以「命令不存在」失败 ⇒ **假红**（而且第一次红就发生在别人最需要信它的时候）。
- **修法**：改用仓内既有惯例的可移植 helper（`scripts/retire-request-logs-v1-family.sh:148`、
  `scripts/verify-migration-checksums.sh:58`）：`sha256sum` 优先、`shasum` 兜底。
- **回归**：改后复跑三组 harness，仍 **13/13 全被抓住**，且基线 SHA 与改前逐字节一致。

---

## 六、修复的机械化（守卫自己也要被审计）＋ 变异验证

新增 `tests/ci_carrier_ir_conversion_wiring_test.sh`（挂进 `guards-gate-scripts` ⇒ 进 CI 快门）
⇒ 把「接线了却跑不到」从**口号**变成**可执行断言**：

| # | 判据 |
|---|---|
| ① | `.workflow/` 下至少有一个流水线配置（读不到 ⇒ **fail-closed**，不静默跳过） |
| ② | 每个流水线配置里有**非注释行**调用 `make guards-fast` |
| ③ | `Makefile` 的 `guards-fast` 与 `guards` 前置里都含 `guards-ir-conversion` |
| ④ | `guards-ir-conversion` 的配方里逐个调用了那三个 harness |
| ⑤ | 三个 harness 脚本本身存在 |

**变异臂（合成夹具）**：非注释行的 `make guards-fast` 必须被检出（正控）⇒ 挪进注释后必须判红（负控）。

**实仓变异验证（三件套）**：

| 步骤 | 结果 |
|---|---|
| ① 变异生效的独立证据 | 把 `guards-ir-conversion` 从 `guards-fast` 前置里摘除（Makefile SHA 变化） |
| ② 门变红 | `PASS=11 FAIL=1`，报「IR 门不在CI 快门（真正在跑的载体入口）链路上」 |
| ③ 还原后 SHA 复核 + 正向复跑 | SHA 与基线逐字节一致 ⇒ 正向复跑恢复绿 |

**交叉验证**：把该门从 `guards-gate-scripts` 里摘掉 ⇒
`tests/gate_scripts_caller_registry_test.sh` 报「它只是**一段脚本**，不是门（LESSONS §507）」
⇒ ★ 证明「新脚本确实被登记册门纳管」。

---

## 七、快门链路健康度（34 门逐门摸排）＋ 两个红的定性

`make guards-gate-scripts` 的 34 道门逐个单独跑：**32 PASS / 2 FAIL**。

| 红项 | 定性 | 证据 |
|---|---|---|
| `tests/deploy_local_contract_test.sh`<br>`FAIL build lock was not released after the early collision` | ★ **既有环境红，与本轮零因果** | ① 在**未改动的远端提交** `5a4f3e71f` 上**逐字复现同一行红**（对照树建在同父目录兄弟处，symlink 解析一致）② 根因＝本机 agent PATH 上的 `rm` 是 **mavis-trash 可恢复删除 shim** ⇒ `lock_release_build` 的递归删目录被接管 ⇒ 锁目录留在 ⇒ 断言红（★ 代码路径正确）③ 本仓 `AGENTS.md:1626` / `AGENTS.md:509` / `docs/12小时内修订审计-2026-11-03-0735.md` P2-7 **早已登记为环境红** |
| `tests/deploy_lib_candidate_chain_single_source_test.sh`<br>`FAIL A4 deploy_lib_candidates() is defined more than once` | ★★ **本轮验证方法自身造成的假红**，已消解 | 该门 A4 判据是**仓根全量扫描** `deploy_lib_candidates() {` 的重复定义 ⇒ 它撞上了我临时建的**嵌套 worktree**（`.worktrees/ci-verify/`）里的那份 checkout ⇒ ★ **移除该嵌套 worktree 后复跑即绿**（`OK A4 deploy_lib_candidates() is defined in exactly one file`）⇒ ★ **不是产品缺陷、不是门缺陷，是验证方法污染了扫描面** ⇒ ★★ 附带纪律：**验证用的 checkout 必须建在仓根之外**（且同父深度以保证 symlink 解析一致） |

⇒ ★ 本轮**没有**修这两条红：前者不在本轮范围（且本仓已登记为环境红）；后者已由移除嵌套 worktree 自然消解。

---

## 八、诚实边界（**未**验证 / **不**归本轮）

1. ★★ **本记录不是 GitHub Actions 的绿灯**：远端是 Codeup ⇒ `.github/workflows/` 从不执行（§二）。
2. ★ **工具链版本不一致**：工作流声明 `go-version: 1.23`，本机实测 `go1.27.2` ⇒ 本记录的读数**不等价于** 1.23 下的读数。
   （本仓 `AGENTS.md` 已登记 gofmt 1.27.x 对齐伪影 131 文件的工具链版本差异。）
3. ★ **4 核 Linux 容器的实测读数待 CI 复测**：本记录所有耗时均为 macOS/darwin-arm64 观测；
   冷缓存下 `guards-ir-conversion` 的上界估 ≈46s（依据：冷缓存单包 `go test ./internal/ir/...` 实测 6.7s 墙钟），
   **该估算是上界估计，不是实测** ⇒ 仓内既有做法同款（「4 核 Linux 的新读数待 CI 复测」）。
4. ★ **部署类门在仓内子目录的 worktree 里不具代表性**（`scripts/deploy-lib` 相对 symlink）⇒ 本记录的对照树因此建在**仓外同父深度**。
5. ★ **未验证**：前端构建、`go vet ./...` 全量、`govulncheck`、`make guards` 慢门（`guards-ursmcheck` 实测 1122s）、
   `verify.sh` 全文执行（只接线了其中的 IR 段，未整跑）、Codeup 平台侧的流水线实际触发。
6. ★ **`docs/` 证据的体量**：两份 transcript 共约 180KB 入库（放 `docs/audit/evidence/`）——
   ★ 该目录是仓内既有的证据落点惯例（已有 `.md`/`.json`/`.tsv`/`.sql` 同类文件）。
7. ★ 本记录与其后的入库提交**只含文档与证据**，不含产品代码改动 ⇒ §三 Run B 的结论对该提交继续有效。

---

## 九、复现方法

```bash
# 0) 取远端真值
git fetch origin && git ls-remote origin refs/heads/main

# 1) 在**仓外同父深度**建对照树（symlink 解析与主树一致）
git worktree add --detach ../ci-verify-clean <目标 SHA>

# 2) 逐字镜像工作流执行（生成 transcript）
bash scripts/audit/ci_mirror_runner.sh \
     "$PWD/../ci-verify-clean" \
     "$PWD/docs/audit/evidence/<本次>.txt" \
     <完整 40 位 SHA>

# 3) 单独复核 CI 快门链路
make guards-gate-scripts          # 34 道门
make guards-ir-conversion         # IR 门（13 臂变异 + 覆盖率门禁）
```
