# 批判式复审：stash@{0..4} 清理核验（2026-09-26）

## 0. 审计对象与结论

- 对象：2026-09-26 凌晨轮对 stash@{0..4} 的"等价性核验"（结论当时为：五条全部被 main 吸收/超越，可安全 drop，待 owner 批）。
- 复审性质：对该核验本身的批判式审计——不重做业务判断，先审查证据链是否有洞。
- **复审结论：原判定（五条可 drop）维持，但首轮证据链有三处实质性盲区，原"已验证"状态不成立；本轮补齐后判定才具备成立资格。** 另发现 09-22 探针记忆一处失真并勘误。

## 1. 三盲区与补查结果

### 盲区①：未跟踪组件（^3）完全未查 —— 差点漏掉 22KB 草稿 SQL

`git stash show` / `git stash show -p` 只覆盖 WIP 树（^）与索引（^2）的差异，**不显示未跟踪文件组件（^3）**。首轮全部结论建立在 `git stash show` 之上，结构性漏查。

补查（`git rev-list --parents -n 1 stash@{N}` 判父提交数）：

| Stash | 父数 | ^3 内容 |
|---|---|---|
| @{0} | 2 | 无 |
| @{1} | 2 | 无 |
| @{2} | 2 | 无 |
| @{3} | 3 | **4 个草稿 SQL（727/728 up+down，最大 22KB）+ 2 草稿文档 + 2 草稿代码 + 12 个运行垃圾文件** |
| @{4} | 3 | 空树 |

stash@{3}^3 逐项定性：
- 草稿 SQL 727/728：被终编 733/734 超越。终编实体在 HEAD **双落位**（`sql/migrations/startup/733/734_*.sql` + `installer/cmd/llm-gw-installer/embeddata/startup/` 同名文件），db-changelog:617 记 733 `applied+verified`。727→733 diff 285 行是**真实设计演进**（去 default 分区；回填候选补 `session_turns_hot` 防热表 30K 缺行——与 session-details-v3 记忆"四缺陷"吻合）；728→734 仅 24 行且基本是重编号。草稿无保留价值。
- 草稿代码 details_writer.go / s1b_fields.go：与 HEAD 函数签名逐一相同（6/6、8/8），纯 body 演进，HEAD 为超集方向。
- 垃圾文件：58MB 构建二进制（run/gateway.build）、raw-logs jsonl、run/ 运行态——本就不应进仓。
- 附注：草稿迁移号 727 已被 `727_sql_audit_slow_query_indexes.sql` 占用（changelog:614），终编改号 733/734。

### 盲区②：只验证"新增吸收"，未验证"删除落地"

等价性是双向命题：stash 删掉的行若 HEAD 还在，drop 同样丢失删除意图。补查全部实质删除行：HEAD 残留仅 9 行，逐行 eyeball 全良性——
- `}`、`//` 语法碎片（grep 噪声）；
- deploy-local.sh 的 `rm -f` 旧站点：HEAD:617-725 已有 /bin/rm + mavis-trash stdout 隔离的加固版（2026-09-23 实测教训落地）；
- request_logs_view_schema 的旧 `FROM session_turns`：属双形态回退分支（727-less 库回退 710 形态）按设计保留；
- view_schema_v2_contract_test 引 710 文件名：同上，双形态契约。

删除意图无丢失。

### 盲区③：stash@{4} 单文件等同外推整条 stash

首轮只确认 deploy-154.sh 与 HEAD blob 一致即判"全吸收"。补齐七文件 blob 对比：**3 个实质脚本（deploy-154/245/seamless.sh）与 e5879c497 逐字节全等**，差异仅 VERSION 系列版本戳；^3 为空树。判定从"行级吸收"升级为"字节级实锤"。

## 2. 连带勘误：09-22 探针超时记忆失真

复审 stash@{4} 时顺带发现。09-22 记忆称"恢复 180s 默认"，git 实证时间线：

- e5879c497（09-20 09:38）：三脚本统一 180→120（即 stash@{4} 的内容，stash 创建仅早 9 分钟）；
- 527b8f511（09-22 15:46 "修正部署错误。"）：**只改 deploy-245.sh** 120→180（+cmd/gateway/main.go 5 行）；
- d5eeb71eb（09-23 18:32）：245 再升 **600** 对齐 systemd 预算；
- **deploy-seamless.sh 与 deploy-154.sh 的默认至今仍是 120s，从未回滚。**

"禁再收紧"的规则成立，但"180s 重新成为默认"不成立。**遗留风险：154 部署至今运行在 120s 默认下，暴露在两次压垮 245 的同一失败模式（大表 ensure 链 90-150s）**——154 与 245 共享 252 PG，冷启动 ensure 同量级。是否上调 600 由 owner 决策（改动部署默认值需谨慎，见 deploy-scripts-port-rotation-fix 记忆的 13 份脚本副本陷阱）。

## 3. 方法论沉淀（后续 stash 清理必须遵守）

1. **三组件全覆盖**：判定前先 `git rev-list --parents -n 1 stash@{N}` 数父提交；有第三父必查 `stash@{N}^3`（未跟踪文件）。`git stash show` 天然不含 ^3，是结构性盲区。
2. **两方向判定**：新增行（`diff ^..stash` 的 + 行）查 HEAD 吸收；删除行（- 行）查 HEAD 是否同样删除。单向不构成等价。
3. **逐文件 blob 判定，禁单文件外推**：`git rev-parse stash@{N}:f` vs `HEAD:f` 全量对比，实质文件逐一过。
4. **垃圾文件不构成保留理由**：构建产物、运行态、raw-logs 在 ^3 中出现时直接忽略，但要写明，防"看起来有文件"造成误保留。

## 4. 复审证据命令（可复跑）

```bash
# 父提交数（判 ^3 存在）
git rev-list --parents -n 1 'stash@{N}'
# ^3 清单与 HEAD 在位性
git ls-tree -r --name-only 'stash@{N}^3'
# 逐文件 blob 等同
git rev-parse 'stash@{N}:f' 'HEAD:f'
# 删除行落地检查
git diff 'stash@{N}^' 'stash@{N}' -- f | grep '^-'   # 对照 HEAD 是否同样删除
# stash@{4} 全吸收锚点
git rev-parse e5879c497:scripts/deploy-seamless.sh 'stash@{4}:scripts/deploy-seamless.sh'  # 同哈希
# 探针默认时间线
git show 527b8f511 -- scripts/deploy-245.sh
git log --oneline -- scripts/deploy-seamless.sh
```

## 5. 处置状态

- 五条 stash：判定不变（可 drop），**仍未执行**，等 owner 批准——本审计不改变处置动作，只补齐判定资格。
- 09-22 记忆：已勘误（memory/2026-09-22-245-deploy-probe-timeout-revert.md 追加 09-26 勘误段）。
- stash 核验记忆：已重写为两轮版本，含方法论四条。
- 154/seamless 探针 120s：遗留风险挂账，不动脚本，等 owner 决策。
