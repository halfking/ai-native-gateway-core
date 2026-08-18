# 2026-08-18 — URSM k2 迁移 L1 完成与审计

## 需求与范围

按批准计划完成 L1 canonical key primitives（vertical TDD 9 循环）与 Slice 0b 审计收口。改动仅限 Migration owner 移交文件 + 两处字节等价收敛（manager.go RecordRequest 段、index/index.go key 构造，均经 doc 14 §10.2 记录授权）。

## 提交

- `07ee022d9` docs(sessionv4): freeze migration owner, ledger id and topology（Slice 0）
- `834cf5d0f` refactor(ursm): converge key construction into store authority（Slice 0b）
- `88c830f2f` feat(ursm): add k2 canonical key primitives (L1)
- `7224f1e13` fix(ursm): apply L1 dual-axis review findings

## L1 产物

- `store/keys_k2.go`：`K2NodeKeyForTenant`/`K2WindowKeyForTenant`/`K2CandidateIndexKey`（error-returning，空 tenant/cid≤0/空 raw/非法 bucket/空分量构造期拒绝）、`ParseNodeKeyAny`（schema origin；k2 分支严格：段数=3、canonical base64url 重编码回检、canonical 十进制 cid、无 rejoin 无 fallback）。
- `store/keys.go`：`ParseNodeKey` 前置拒绝 `k2` marker——实证修复：tenant=`\xd3\x5d\xb7`（b64="0123" 全数字）的 k2 key 曾被宽松分支以 `{tenant:"k2",cid:123}` **ok=true** 误解析。
- `store/schema_mode.go`：`KeySchemaMode`（零值 legacy、boot-only、独立于 URSM_V2_MODE）。
- `index/index.go`：委托 `store.CandidateIndexKey`（字节等价，消除双实现）。
- 测试 10 个新增，全部先 RED 后 GREEN；golden bytes 由 python3 独立推导并与 doc 16 向量交叉验证一致（YQ/Yjo4OmM/YTo3OmI/Nw/bQ）；legacy exact bytes 契约测试零修改通过。

## 审计（双轴 + 门禁）

- Standards 轴：无硬性违规；3 个判断题已修（错误前缀统一 `ursm.v2:`、candidate 校验有序化、bucket 校验注释）。
- Spec 轴：(a) grammar 逐字 ✓ (b) 校验完备 ✓ (c) k2 严格 + legacy 不吞 k2 ✓ (d) tenant="k2" 拒绝符合 §4 ambiguous 语义 ✓ (e) mode 类型 ✓ (f) index 字节等价 ✓ (g) 无 scope creep ✓ (h) 无完成式表述 ✓。
- 门禁：`go vet ./...` ✓、`go build ./...` ✓、`scripts/pre-commit-check.sh` PASS(4) ✓、`scan-secrets.sh --mode=strict` 0 findings ✓。
- `go test ./... -count=1`：204 包 ok，1 失败 = `domains/dispatch TestPipelineWiresQueueMirror`——**既有 flaky**（`go list -deps ./domains/dispatch` 零 ursm 依赖；隔离重跑 3/3 通过；全仓两次运行中一次通过一次失败）。非本 diff 引入。
- `go test -race ./...`：204 包 ok，同一 dispatch flaky；**无 DATA RACE**。
- 早前一次全量运行中 `domains/health TestTCPChecker_RealWorldScenario/Cloudflare_DNS` 失败为沙箱网络瞬时问题，后续两次运行（full+race）health 均 ok。
- miniredis 未参与 L1（纯函数）；真实 Redis smoke `TestRecordRequestLuaSmokeOnRealRedis` 在无 `TEST_REDIS_URL` 环境按惯例 SKIP——不构成任何真实 Redis 证据。

## 并行会话冲突记录（合并前必须裁决）

本会话执行期间，主工作树 `main` 出现另一会话的提交：`4cb595a83`（doc 15 实施计划）→ `123de7e15`（merge）→ `0a0e94ead`（**在 doc 14 §0 记录了另一格式 ledger_id `ursm-k2-mig-134e6d21-721b-41d4-af0b-adff143107e2`**）；未跟踪草稿 doc 16（测试矩阵，其 API 命名 `NodeKeyCanonical/ParseNodeKeyCanonical/SchemaK2` 与本分支 error-returning `K2NodeKeyForTenant/ParseNodeKeyAny/KeySchemaK2` 不同；其自称"函数名是测试定义的外部契约，实现由 owner 决定"）。分歧点：

1. **ledger_id 双记录**：doc 14 说 ledger_id 不可复用；main 的 §0 与本分支的 §10 各记一个。合并时必须二选一并在 changelog 留痕，由用户裁决。
2. **API 命名**：本分支按用户批准计划实现；doc 16 为未提交草稿。若采纳 doc 16 命名需重命名（机械改动）。
3. 本分支基点 `fafdc60a5` 落后 main HEAD `0a0e94ead` 4 个提交（均文档/脚本，无 `domains/ursm/**` 代码冲突）；合并前需 rebase。

doc 15/16 的实质内容（standalone 拓扑、round-trip 硬闸、状态机、G4 边界）与本计划一致，L3 将采纳 doc 15 §2 的 round-trip 重编码硬闸与 field_checksum 序列化规则。

## 裁决不变

`M5-0/T0 = BLOCKED / NO-GO`。L1 完成不构成 G1；G1 判定只能在 L2/L3 完成后按 doc 14 §7 全门槛由独立审计作出。下一步：L2 schema-aware store/recovery。
