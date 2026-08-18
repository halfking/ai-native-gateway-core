# 16 · URSM delimiter-safe key 迁移测试矩阵与 TDD 顺序

> **状态**:测试计划文档(审计修正版);不改变 [13 号](./13-T0契约冻结与所有权.md)/[14 号](./14-URSM%20Redis%20delimiter-safe%20key兼容迁移冻结决策.md)/[15 号](./15-URSM%20delimiter-safe%20key迁移实施计划.md) 的任何裁决,不解除 `M5-0/T0 = BLOCKED / NO-GO`。
> **事实基线**:冻结基线 `df6c65463`(14 号);本矩阵探索与审计复核时 HEAD `51223fd37` → `123de7e15`(main 与 origin/main 一致)。`domains/ursm/v2/{store,recovery,persist,index}` 与 `scripts/rollback/**` 自冻结基线起无代码变动(`git log df6c65463..HEAD -- <path>` 为空,已核验),故全部 `path:line` 引用按冻结基线有效。
> **定位**:与 15 号实施计划配套——15 号回答"怎么迁"(模式/状态机/裁决清单),本文件回答"按什么顺序写测试、每个测试依赖什么层级、什么证据算数"。首个 RED 测试与 slice 顺序供 Migration owner 执行;**14 号 §0 前置事实已在 origin/main(`50258a382`)记录**(owner=`halfking`、ledger_id=`ursm-k2-mig-134e6d21-721b-41d4-af0b-adff143107e2`、拓扑=standalone、L1 移交 `keys_k2.go`/`keys_k2_test.go`),L1 实施禁令已解除;`M5-0/T0 = BLOCKED / NO-GO` 裁决不变。

## 1. 写第一个测试前的两个前置条件

两项前置条件**均已满足**(14 号 §0,2026-08-18 记录):

1. **Owner / ledger / 拓扑**:Migration owner = `halfking`;ledger_id = `ursm-k2-mig-134e6d21-721b-41d4-af0b-adff143107e2`(一次性签发,不得复用);目标拓扑 = **standalone**,k2 grammar 不引入 hash-tag(未来迁 Sentinel/Cluster 须按 14 号 §2 签发新 marker 与新 ledger_id)。
2. **文件移交**:L1 仅移交 `domains/ursm/v2/store/keys_k2.go`(新增)与 `domains/ursm/v2/store/keys_k2_test.go`(新增)两个精确文件;`keys.go`、manager/recovery/persist 等既有 URSM 文件**仍未移交**——本矩阵 slice 6-10 涉及的既有测试文件,在获得扩充移交前由 URSM owner 执行(见 §3 备注)。

## 2. 首个 L1 RED→GREEN 测试

**文件**:新建 `domains/ursm/v2/store/keys_k2_test.go`(package `store`;14 号 §0 移交记录的精确列名文件),与 `keys_t0_contract_test.go` 同层同风格——纯函数 exact-bytes 契约,无需 miniredis。既有 `keys_t0_contract_test.go` 的 legacy exact bytes 断言保持不动(14 号 §1.3:legacy 精确字节是兼容契约)。

**内容**:canonical node key 的 golden bytes + round-trip + 14 号 §1 冻结的历史 collision pair。golden bytes 按 14 号 §2 grammar 以 `base64.RawURLEncoding` 计算,已程序化复核(`printf|base64|tr '+/' '-_'`):

```go
func TestCanonicalNodeKeyK2RoundTripAndFrozenCollisionPair(t *testing.T) {
	const prefix = "contract:"
	// 14 号 §1 冻结的 collision pair:legacy 下生成同一 key 的两个 tuple
	a := NodeKeyCanonical(prefix, "a", 7, "b:8:c")   // base64url: a→YQ  b:8:c→Yjo4OmM
	b := NodeKeyCanonical(prefix, "a:7:b", 8, "c")   // a:7:b→YTo3OmI  c→Yw
	if want := "contract:node:k2:YQ:7:Yjo4OmM"; a != want {
		t.Fatalf("canonical a = %q, want %q", a, want)
	}
	if want := "contract:node:k2:YTo3OmI:8:Yw"; b != want {
		t.Fatalf("canonical b = %q, want %q", b, want)
	}
	if a == b {
		t.Fatal("frozen legacy collision pair must not collide in k2")
	}
	pa, sa, ok := ParseNodeKeyCanonical(prefix, a) // strict parser 必须返回 schema 来源
	if !ok || sa != SchemaK2 || pa.TenantID != "a" || pa.CredentialID != 7 || pa.RawModel != "b:8:c" {
		t.Fatalf("k2 parse = %+v schema=%v ok=%v", pa, sa, ok)
	}
	// numeric tenant 无需 t: 标签——b64url 本身无歧义
	if got := NodeKeyCanonical(prefix, "7", 7, "m"); got != "contract:node:k2:Nw:7:bQ" {
		t.Fatalf("numeric tenant canonical = %q", got) // 7→Nw  m→bQ
	}
}
```

函数名(`NodeKeyCanonical`/`ParseNodeKeyCanonical`/`SchemaK2`)是测试定义的外部契约,实现由 owner 决定;当前代码无这些符号,测试天然 RED。RED 的机器证明:现有 `NodeKeyForTenant`(`store/keys.go:15-27`)对该 pair 产出**同一个** legacy key `contract:node:a:7:b:8:c`,且 `ParseNodeKey`(`store/keys.go:33-66`)无法恢复原 tuple。断言全部落在"key 字节 + parse 结果 + schema 来源"外部可观察输出上,不触及内部实现。

## 3. TDD vertical-slice 顺序

每个 slice = 一条外部可观察行为的失败测试 → 最小实现转绿。顺序遵循 14 号 §7 必测清单与 §8 的 L1→L2→L3→G1 链:

| # | 层 | 行为(外部可观察) | 落点 | 依赖 |
|---|---|---|---|---|
| 1 | L1 | canonical node golden bytes + round-trip + 冻结 collision pair(§2) | `store/keys_k2_test.go` | 纯函数 |
| 2 | L1 | window key golden bytes(`<prefix>win:k2:<bucket>:<b64tenant>:<cid>:<b64model>`)+ round-trip;bucket 仅 `1m/5m/30m`,非法 bucket 构造/parse 拒绝 | 同上 | 纯函数 |
| 3 | L1 | candidate index golden bytes + round-trip;**`index` 包**在 miniredis 上以 colon-bearing tenant/canonical 做 Upsert→Query 往返与隔离,并用 `mr.Keys()`/直接读断言实际写入的 key 字节等于 canonical grammar——堵住 `index/index.go:33` 与 `store.CandidateIndexKey`(`store/keys.go:110-112`)重复拼接的第二套 namespace(index 包当前无 production caller,与 14 号 §2 一致,已 grep 复核) | `store/keys_k2_test.go`(store 侧)+ `index/index_test.go` 扩展(URSM owner/另行移交) | 纯函数 + miniredis |
| 4 | L1 | parser 严格性:segment 数错、非 base64url、credential 非正十进制、未知 marker、k2 空 tenant 一律失败;`ParseNodeKey` 对 legacy 返回 `SchemaLegacy`、对 k2 返回 `SchemaK2`,禁止宽松 fallback | `store/keys_k2_test.go` | 纯函数 |
| 5 | L1 | collision property:代表性 corpus(`:`、重复/前后 delimiter、numeric tenant、Unicode、空值)满足"不同 tuple → 不同 key"且 round-trip 还原;历史 collision pair 集不复现 | `store/keys_k2_test.go`(corpus 以表驱动固化;新增文件须先扩充移交) | 纯函数 |
| 6 | L2 | schema mode=canonical/dual 下 authoritative 空 tenant 仍 fail-closed 且不写 legacy key(扩展 `manager_t0_contract_test.go` 既有模式) | `domains/ursm/v2/manager_t0_contract_test.go` | miniredis |
| 7 | L2 | colon-bearing tenant/model 的 cross-tenant/model isolation:`RecordRequest`、`FilterAndScore`、NodeMirror、dedup、candidate index 在 `("a",7,"b:8:c")` vs `("a:7:b",8,"c")` 下互不可见、互不更新 | `manager_t0_contract_test.go` / `manager_filter_test.go` 扩展 | miniredis |
| 8 | L2 | dual-read precedence:legacy-only、canonical-only、both-present(canonical 优先,仅 ledger `migratable` 同 tuple 允许 legacy fallback)、conflict 拒绝;node + 3 window + dedup 作为同一 key set 一致读写(禁混配,14 号 §5.2.2) | `store/` schema-aware store 测试 | miniredis |
| 9 | L2 | persist/recovery schema-aware:`persist.Collect`(`persist/writer.go:79` 现只认 legacy)识别双 schema、ambiguous legacy key 跳过告警不进 authoritative;recovery 扫描(`recovery/manager.go:156`)识别 k2;coverage manifest 只收 canonical、pending marker fail-closed(扩展 `recovery/coverage_test.go` 三既有测试) | `persist/writer_test.go`、`recovery/coverage_test.go` 扩展 | miniredis(db=nil 只测 Collect) |
| 10 | L2 | ready gate:canonical-only coverage 完整才开门;legacy-only 或 k2 缺失即关门(沿用 `WarmupFromCoverage` 先置 ready=false 再验证,`recovery/manager.go:231-246`) | `recovery/coverage_test.go` 扩展 | miniredis |
| 11 | L3 | preflight 四类分类(`migratable`/`canonical_present`/`ambiguous`/`excluded_non_authoritative`)+ inventory checksum;15 号 §2 的 round-trip 重编码硬闸;ambiguous/conflict>0 ⇒ NO-GO | Migration owner 新包 + 测试(ledger 列名移交后) | miniredis |
| 12 | L3 | copy + PTTL:`-2` 不复制、`-1` 保留 persist、正值扣减 `PEXPIRE` 且绝不延长;generation/field-checksum fencing、幂等 marker、中断重跑恢复 | 同上 | miniredis |
| 13 | L3 | cleanup:仅按 ledger exact key + checksum 删除、限速/暂停/重跑;负向测试"不在 ledger 的 key 不被删";rollback 后不删证据 | 同上 | miniredis |
| 14 | G1 前 | 双写/copy Lua 真实 Redis dialect smoke(复用 `store/record_request_test.go:639-654` 的 `TestRecordRequestLuaSmokeOnRealRedis` 模式:唯一前缀 + `TEST_REDIS_URL` gate) | 对应包 real-smoke 测试 | **真实 Redis** |
| 15 | G1 | 门禁全量(§5) | — | 见 §5 |

所有权备注:slice 1-5 落在 14 号 §0 已移交的 `keys_k2.go`/`keys_k2_test.go`(Migration owner);slice 6-10 的落点(`manager_t0_contract_test.go`、`manager_filter_test.go`、`store/` schema-aware 测试、`persist/writer_test.go`、`recovery/coverage_test.go`)及其对应生产文件均未移交——由 URSM owner 执行,或按 14 号 §0 机制扩充移交后再交 Migration owner;slice 11-13 的 L3 新包属 Migration owner 自有范围。

## 4. 依赖层级边界:miniredis / sqlmock / 不可达地址仅证明 L1/L2

13 号 §7 与 14 号 §1.5 冻结此边界。落到本仓:

- **miniredis**(store/recovery/persist/manager/index/cache 全部既有测试)只证明构造、parse、precedence、gate 逻辑(L1/L2/L3 逻辑层)。其 Lua 是模拟实现,**不能证明**真实 Redis Lua dialect、`NOSCRIPT`/script 缓存丢失、restart 语义、Pub/Sub(`store/keys.go:144-146` 失效通道)真实投递、PTTL 实时扣减精度。
- **跨 key 原子性不可证明**:node+3 window+dedup 的 Lua 双写原子性、cluster hash-tag 是否同 slot,miniredis 单实例无法证明(14 号 §5.2.4、15 号 §3)——只能 G4。
- **不可达地址 failure injection**(`deadRedisClient`,`manager_filter_test.go:42-45`)只证明 fail-closed 错误路径,不证明真实重连/部分故障。
- **sqlmock**:本仓用于 `bg/*`、`internal/outbox`、`domains/hooks/handoff`、`metatools` 等;**ursm v2 内无 sqlmock**(已 grep 复核)。persist 的 PG 侧靠 `db=nil` 跳过(`persist/writer_test.go`);lease/fencing 语义必须真实 PG——migration 532 头注释"单元测试只能钉 SQL 形状"同理。

## 5. G1 / G3 / G4 门禁:命令、环境变量、依赖、证据工件

**G1(M5-2 schema & migration gate)— 全本地,无强制真实依赖**(14 号 §7 末段):

- 命令:相关包 targeted test → 全仓 `go test -count=1 ./...` → `go test -race ./...` → `go vet ./...` → `go build ./...` → pre-commit → scoped secret scan → 独立双轴 review。
- 环境变量:无必需;`TEST_REDIS_URL` 可选(slice 14 Lua smoke 门控)。
- 工件:各命令完整输出、review 记录、ledger 中 owner/移交/编号记录。

**G3(M5-4 authoritative local closure + M5-1 合流回归 + mode/revert 演练)— 仍全本地**:

- targeted test + 全仓四件套重复;legacy→dual→canonical→rollback 本地切换演练(先关 ready gate 的顺序验证)。证据按 13 号 §7 只算 L1/L2/L3 closure。

**G4(M5-5 真实依赖证据)— 三块独立必需**:

1. **隔离真实 Redis**(专用一次性实例):`TEST_REDIS_URL`(默认 `127.0.0.1:6379`,沿用 `realRedisClient` 模式 `manager_filter_test.go:25-40`:DialTimeout 500ms、Ping 失败 Skipf、唯一前缀 `ursm:v2:test:<nanos>:`)。场景:dual-write/cutover/rollback 全程、真实 restart、`NOSCRIPT`(SCRIPT FLUSH 后重放)、连接重建、Pub/Sub、真实 TTL/cleanup、coverage warmup。拓扑已裁决 standalone(14 号 §0),Cluster 专属项(CROSSSLOT/hash-tag/failover)不适用。gated 用例必须实际运行、零 SKIP。
2. **真实 PG lease/fencing**:两途均可——(a) `TEST_PG_URL` 指一次性库 + `//go:build integration`(migration 532 约定);(b) testcontainers(repair_529 先例;当前 vendor 集合下 `go vet -tags=integration ./sql/migrations/startup/` 通过,2026-08-18 复核,需 Docker daemon)。注意 `cmd/test_sql` 用的是独立变量 `LLM_GATEWAY_TEST_PG_URL`。
3. **真实 provider / slow-client / chaos**:`tests/integration/request_flow_fault_injection_test.go` 模式(`TEST_PG_URL` + `TEST_REDIS_URL` 双 gate、`//go:build integration`)+ llm-gateway-test skill(网关级真实环境功能矩阵:协议 × 模型 × 错误路径 × 性能;环境隔离、key 走环境变量、JSON/MD/HTML 报告工件)。

- 工件:带 skip 状态的 `-v -count=1` 全量输出(gated 用例 0 SKIP)、preflight inventory + checksum、ledger checkpoint 时间线、rollback drill 记录、skill 格式报告。G5(GO/NO-GO)全齐后裁决;T8/245 仅 GO 后执行。

## 6. SKIP / 不可达依赖 / 既有失败的显式报告规则

规则(13 号 §7.4、14 号 §7 末段):

1. 每个 workstream 报告逐条列出:每个测试的真实/模拟依赖、全部 SKIP 及原因;SKIP 一律带地址与错误的显式格式(如 `t.Skipf("TEST_REDIS_URL (%s) unreachable: %v", addr, err)`),禁止静默 `t.Skip()`。
2. 证据运行用 `-count=1 -v`;G4 报告显式声明 gated 测试"已实际运行且零 SKIP",否则按未验证计 NO-GO。
3. 不可达依赖不得以 mock 替代后写成 release-ready。

**当前基线清单**(2026-08-18 复核,HEAD `123de7e15`):

- 真实 Redis gated、不可达即 SKIP 的 3 处:`domains/ursm/v2/probe_test.go:202`、`manager_filter_test.go:36`、`store/record_request_test.go:654`;另有 `tests/integration/request_flow_fault_injection_test.go`(`//go:build integration` + 双 env gate)。
- 真实 PG gated:`sql/migrations/startup/migration_532_integration_test.go:111,125`(`TEST_PG_URL` not set / unreachable → Skip)。`repair_529_integration_test.go` 是 `//go:build integration` + testcontainers(依赖 Docker daemon,无 env skip 路径)。
- 永久遗留 SKIP:`test/events/contract` 的 6 个 `TestRequestCompletedV1_*`(outbox 交付路径未启用);与 k2 无关,不得计入或顺手"修复"。
- 既有失败:无。实测 `store`/`recovery`/`index` 包全绿;`test/events/contract` 7 个可运行测试全 PASS(publisher 分析测试已 PASS,`test/events/README.md` 状态已随本文件同步修正)。

## 7. 审计记录:对 2026-08-18 会话初版矩阵的更正

初版以会话文本交付,落盘为本文档时经复核更正四处:

| # | 初版错误 | 更正(含验证) |
|---|---|---|
| A1 | "当前 main(`df6c6546`)的 key 构造仍是原文拼接"——把 14 号冻结基线误当探索时 HEAD | 冻结基线 `df6c65463`;探索/复核 HEAD `51223fd37`→`123de7e15`;key 文件自冻结基线无变动(`git log df6c65463..HEAD -- domains/ursm/v2/store/keys.go ...` 为空) |
| A2 | "repair_529_integration_test.go 同样 TEST_PG_URL not set → Skip" | 实为 `//go:build integration` + testcontainers(`postgres.Run` + `container.ConnectionString`),无 env skip 路径 |
| A3 | 引用 migration 532 头注释"testcontainers 在当前 vendor 集合下无法编译"并写进 G4 建议"勿引入" | 当前 HEAD 下 `go vet -tags=integration ./sql/migrations/startup/` 通过,testcontainers 可用;G4 PG 证据两途均可(§5)。532 头注释已同步修正 |
| A4 | 将 `cmd/test_sql` 归入 `TEST_PG_URL` gate 族 | 实为独立变量 `LLM_GATEWAY_TEST_PG_URL`(`cmd/test_sql/main.go:14-18`) |

复核通过项:base64url golden bytes(`YQ`/`Yjo4OmM`/`YTo3OmI`/`Nw`/`bQ`/`Yw` 程序化验证);`index` 包无 production caller;全部 `path:line` 引用;测试运行状态;`index/index.go:33` 重复 key 拼接的发现。

## 8. 变更记录

- 2026-08-18:首次落盘;在会话初版矩阵基础上完成审计更正(A1-A4)并复核全部事实;与 15 号实施计划配套。
- 2026-08-18:对齐 origin/main(`50258a382`)的 14 号 §0 前置事实记录——前置条件标记为已满足,首个测试与 L1 slice 落点改为移交列名的 `keys_k2_test.go`,补 slice 所有权备注,G4 cluster 项按 standalone 裁决标记不适用。
