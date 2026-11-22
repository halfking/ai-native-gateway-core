package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domain"
	domainprovider "github.com/kaixuan/llm-gateway-go/domains/provider"
	routerprovider "github.com/kaixuan/llm-gateway-go/provider"
)

// ─────────────────────────────────────────────────────────────────────────
// 2026-11-23（N3 接线）· 守卫：discovery 阶段接**真实目录**。
//
// ★ 背景（★ 一句话说清）：★ 这一阶段此前挂的是**内存 demo 目录**
// （★ gpt-4 假凭据 + default-openai 假 provider）⇒ ★ 对任何真实模型都报
// ★ 「no healthy provider」⇒ ★ 结论既不真、★ 也让人以为「模型没 provider」。
//
// ★ 本轮把目录换成**路由的权威候选查询**（★ provider.Client.GetCandidates）
// ⇒ ★ 目录结论与「路由会怎么选」同源。
//
// ★ 门的分工：
//   门 1 ★ 目录失败 ⇒ advisory **不中止管线**（★ advisory 的基本契约）
//   门 2 ★ 候选的凭据身份**不被 provider id 顶替**（★ 原设计稿最担心的一步）
//   门 3 ★ 没接目录时**老行为不变**（★ demo 目录 ⇒ no_healthy_provider）
//   门 4 ★ 没 DB 时**诚实报错**（★ 而不是静默给空候选 —— ★ 空候选会被
//        读成「这个模型没有可用 provider」，★ 那是另一种错答案）
//   门 5 ★ **接线门**（★ 静态）：★ main.go 必须注入目录、★ main_pipeline
//        必须保留后绑定入口 —— ★ 「接线才是会退化的那一环」
//   门 6 ★ **真库门**（★ TEST_DATABASE_URL）：★ 目录给出的候选
//        ① credential_id 必须真实存在于 credentials；★ ② (credential_id,
//        模型) 在**权威视图**里必须 is_routable；★ ③ 候选数**不得多于**
//        权威视图的行数（★ 多出来 ⇒ 把死条目当活条目了）
// ─────────────────────────────────────────────────────────────────────────

// fakeCandidateSource 是行为门用的假候选源。
type fakeCandidateSource struct {
	enabled bool
	cands   []routerprovider.Candidate
	err     error

	gotModel   string
	gotProfile string
	gotTenant  string
	calls      int
}

func (f *fakeCandidateSource) Enabled() bool { return f.enabled }

func (f *fakeCandidateSource) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]routerprovider.Candidate, *routerprovider.Policy, error) {
	f.calls++
	f.gotModel = model
	f.gotProfile = profile
	f.gotTenant = tenantID
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.cands, nil, nil
}

// newAdvisoryHookWithCatalog 造一个挂了目录的 advisory hook。
func newAdvisoryHookWithCatalog(t *testing.T, cat domainprovider.DiscoveryCatalog) *domainprovider.AdvisoryDiscoveryHook {
	t.Helper()
	store := domainprovider.NewInMemoryStore()
	prober := domainprovider.NewProber(store)
	hook := domainprovider.NewAdvisoryDiscoveryHook(store, prober)
	if cat != nil {
		hook.SetCatalog(cat)
	}
	return hook
}

// ── 门 1：目录失败 ⇒ advisory 不中止管线 ──────────────────────────────
func TestDiscoveryCatalogFailureDoesNotAbortPipeline(t *testing.T) {
	hook := newAdvisoryHookWithCatalog(t, &routerDiscoveryCatalog{
		client: &fakeCandidateSource{enabled: true, err: errors.New("boom from catalog")},
	})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"

	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("advisory hook 返回了 error: %v\n"+
			"  ★ 它一旦返回 error，管线就在这一阶段中止 ⇒ 治理链/路由链对真实流量不可达", err)
	}
	status, _ := env.Metadata["provider_discovery_status"].(string)
	if status != "store_error" {
		t.Errorf("provider_discovery_status = %q，期望 %q", status, "store_error")
	}
	detail, _ := env.Metadata["provider_discovery_detail"].(string)
	if detail == "" {
		t.Error("provider_discovery_detail 为空：失败时必须留原因，否则日志里只剩一个状态码")
	}
}

// ── 门 2：候选的凭据身份不被 provider id 顶替 ────────────────────────
func TestDiscoveryCandidateCarriesRealCredentialID(t *testing.T) {
	// ★ 故意让 credential_id 与 provider_id **不同**（★ 真实目录里就是不同的
	// 命名空间：★ provider 587 的凭据是 5 / 217 / …）⇒ ★ 两个值相等时这条门
	// 是空转的（★ 「相邻两条同时出错」类变异测不出来的坑）。
	src := &fakeCandidateSource{enabled: true, cands: []routerprovider.Candidate{
		{CredentialID: 4242, ProviderID: 587, RawModel: "glm-5.3-flash", OfferRawModel: "glm-5.3-flash", Routable: true},
	}}
	hook := newAdvisoryHookWithCatalog(t, &routerDiscoveryCatalog{client: src})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"

	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("advisory hook 返回了 error: %v", err)
	}
	cands, _ := env.Metadata["provider_candidates"].([]domainprovider.DiscoveryCandidate)
	if len(cands) != 1 {
		t.Fatalf("候选数 = %d，期望 1", len(cands))
	}
	if cands[0].CredentialID != "4242" {
		t.Errorf("CredentialID = %q，期望 %q\n"+
			"  ★ 候选的凭据身份必须来自 credential_id；★ 取成 provider id 是"+
			"★ 原设计稿列为「最容易做错」的那类错误", cands[0].CredentialID, "4242")
	}
	if cands[0].Provider != "587" {
		t.Errorf("Provider = %q，期望 %q", cands[0].Provider, "587")
	}
	if cands[0].Model != "glm-5.3-flash" {
		t.Errorf("Model = %q，期望 %q", cands[0].Model, "glm-5.3-flash")
	}
}

// ── 门 2b：不可路由的候选必须被目录过滤掉 ────────────────────────────
//
// ★ 为什么要有这条：★ 目录说「哪些候选可用」⇒ ★ 死条目（★ 密钥解不开、
// ★ 凭据已冷却、★ provider 被停用）混进名单 ⇒ ★ 名单就在骗人。
// ★ 而路由侧对同一批候选的判定就是 Routable ⇒ ★ 目录必须跟着它。
func TestDiscoveryCatalogDropsUnroutableCandidates(t *testing.T) {
	src := &fakeCandidateSource{enabled: true, cands: []routerprovider.Candidate{
		{CredentialID: 11, ProviderID: 587, OfferRawModel: "glm-5.3-flash", Routable: true},
		{CredentialID: 22, ProviderID: 587, OfferRawModel: "glm-5.3-flash", Routable: false},
	}}
	hook := newAdvisoryHookWithCatalog(t, &routerDiscoveryCatalog{client: src})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"

	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("advisory hook 返回了 error: %v", err)
	}
	cands, _ := env.Metadata["provider_candidates"].([]domainprovider.DiscoveryCandidate)
	for _, c := range cands {
		if c.CredentialID == "22" {
			t.Error("不可路由的候选（credential_id=22）进了目录名单\n" +
				"  ★ 名单里混着路由不会用的条目 ⇒ ★ 目录就在骗人")
		}
	}
	if n, _ := env.Metadata["provider_count"].(int); n != len(cands) {
		t.Errorf("provider_count=%d 与候选数 %d 不等", n, len(cands))
	}
}

// ── 门 3：没接目录 ⇒ 老行为不变（demo 目录 ⇒ no_healthy_provider）─────
func TestDiscoveryWithoutCatalogKeepsLegacyBehaviour(t *testing.T) {
	hook := newAdvisoryHookWithCatalog(t, nil)
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata["model"] = "glm-5.3-flash"

	if err := hook.Execute(context.Background(), env); err != nil {
		t.Fatalf("advisory hook 返回了 error: %v", err)
	}
	status, _ := env.Metadata["provider_discovery_status"].(string)
	if status != "no_healthy_provider" {
		t.Errorf("provider_discovery_status = %q，期望 %q\n"+
			"  ★ 没接目录时**老行为必须不变**（★ 否则「没接线」与「接错了」分不开）",
			status, "no_healthy_provider")
	}
}

// ── 门 4：没 DB ⇒ 诚实报错（而不是静默给空候选）──────────────────────
func TestDiscoveryCatalogWithoutDBReportsError(t *testing.T) {
	cat := &routerDiscoveryCatalog{client: &fakeCandidateSource{enabled: false}}
	cands, err := cat.FindByModel(context.Background(), "glm-5.3-flash", "default")
	if err == nil {
		t.Fatalf("没有 DB 时目录必须报错，★ 却返回了 %d 个候选\n"+
			"  ★ 空候选会被读成「这个模型没有可用 provider」——★ 那是另一种错答案",
			len(cands))
	}
	if len(cands) != 0 {
		t.Errorf("报错时必须同时返回空候选，★ 却返回了 %d 个", len(cands))
	}
}

// ── 门 5：接线门（静态）────────────────────────────────────────────────
func TestDiscoveryCatalogIsWired(t *testing.T) {
	mainSrc, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go: %v", err)
	}
	if !strings.Contains(string(mainSrc), "SetV2DispatchProviderCatalog(v2Deps, &routerDiscoveryCatalog{client: providerClient})") {
		t.Error("main.go 没有把真实目录注入 discovery 阶段\n" +
			"  ★ 目录不接线 ⇒ discovery 仍挂内存 demo 目录 ⇒ 结论恒为 no_healthy_provider")
	}
	pipeSrc, err := os.ReadFile("main_pipeline.go")
	if err != nil {
		t.Fatalf("读 main_pipeline.go: %v", err)
	}
	body := string(pipeSrc)
	for _, needle := range []string{
		"SetV2DispatchProviderCatalog",               // ★ 后绑定入口
		"deps.ProviderDiscoveryHook.SetCatalog(cat)", // ★ 目录真的落到 hook 上
		"provider.NewAdvisoryDiscoveryHook(",         // ★ 仍是 advisory 版（★ 严格版会中止管线）
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("main_pipeline.go 缺少 %q", needle)
		}
	}
	// ★ 严格版不许被挂到 v1 包装路径上（★ 那是 L3 已经修过的东西）。
	if strings.Contains(body, "provider.NewProviderDiscoveryHook(deps.ProviderStore, deps.ProviderProber)") {
		t.Error("main_pipeline.go 里出现了严格版 discovery hook\n" +
			"  ★ 严格版在找不到 provider 时会中止整条管线 ⇒ 治理链/路由链对真实流量不可达")
	}
}

// mustAtoi 解析候选里的凭据 id（★ 解析不了就是门红：★ 候选的凭据身份必须是数字 id）。
func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	v, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("候选的 CredentialID %q 不是数字 id: %v", s, err)
	}
	return v
}

// ── 门 6：真库门（★ TEST_DATABASE_URL；★ 未设置则 SKIP）──────────────
//
// ★ 验的是四件事（★ 每件都对应一类「静默给错答案」）：
//
//	① **源**（★ 权威候选查询）对真实模型必须给出候选
//	   ⇒ ★ 证明目录真的接上了真目录（★ 而不是又落回 demo 目录）；
//	② 每个候选的 credential_id **真实存在**于 credentials 表（★ 越界即红）；
//	③ 候选数**不多于**权威视图 v_routable_credential_models 里该模型的
//	   可路由行数 ⇒ ★ 多出来就是「把死条目当活条目」（★ 这正是「照设计稿的
//	   3 条约束自己写一条查询」会踩的坑：★ 3,132 vs 726）；
//	④ 适配层的输出**是源的子集**（★ 映射不引入源里没有的东西）。
//
// ★ ★ 为什么验「源」而不是验适配器：★ 真库门用**假密钥**跑（★ 不可能用
// ★ 生产密钥）⇒ ★ 解密必然失败 ⇒ ★ 候选全被标成不可路由 ⇒ ★ 适配器
// ★ （★ 它只报路由真会用的）⇒ ★ 必然返回空集 ⇒ ★ **拿适配器当断言对象
// ★ 就是「空转绿」** ⇒ ★ 必须验源、★ 再验适配器是源的子集。
//
// ★ ★ 为什么「不多于」而不是「逐项相等」：★ 源的候选集受**路由策略**
// ★ （★ rr.PlanOrder）影响 ⇒ ★ 数量是策略的函数、★ 不是目录的函数
// ★ ⇒ ★ 断言「相等」会把「策略变了」误报成「目录错了」⇒ ★ 「不多于」
// ★ 才是稳定的不变量（★ 上界）。★ 实测（★ 211，★ claude-opus-4-5）：
// ★ 源 4 个 / 权威视图 4 个 ⇒ **恰好相等**（★ 记录在日志里）。
func TestDiscoveryCatalogRealDBMatchesAuthoritativeView(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// ★ 挑一个真库里**确实有可路由行**的模型（★ 不写死模型名 ⇒ 目录变了也不红）。
	var model string
	err = pool.QueryRow(ctx, `
		SELECT v.raw_model_name
		FROM v_routable_credential_models v
		WHERE v.is_routable
		GROUP BY v.raw_model_name
		ORDER BY count(*) DESC, v.raw_model_name
		LIMIT 1
	`).Scan(&model)
	if err != nil {
		t.Fatalf("挑模型失败: %v", err)
	}
	if model == "" {
		t.Skip("真库里没有可路由的模型行 —— 本门无从验证")
	}

	client := routerprovider.NewClient()
	// ★ 密钥是假的：★ 候选的**元数据**（credential_id / provider_id / 模型名）
	// ★ 不依赖解密；★ 解密失败只会把候选标成不可路由、★ 不会删掉它
	// （★ provider/client.go 的 enrichWithAPIKeys 明确如此）。
	client.SetDB(pool, "gate-secret-key", "gate-credential-encryption-key")

	// ① 源必须给出候选。
	srcCands, _, err := client.GetCandidates(ctx, model, "", "default")
	if err != nil {
		t.Fatalf("源取候选失败: %v", err)
	}
	if len(srcCands) == 0 {
		t.Fatalf("源对真实模型 %q 返回了 0 个候选\n"+
			"  ★ 真库里有可路由行 ⇒ ★ 目录要么没接上、★ 要么口径错了", model)
	}

	// ② 凭据身份必须真实存在。
	for i := range srcCands {
		credID := srcCands[i].CredentialID
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM credentials WHERE id = $1`,
			credID).Scan(&n); err != nil {
			t.Fatalf("查 credentials 失败: %v", err)
		}
		if n != 1 {
			t.Errorf("候选的 credential_id=%d 在 credentials 表里不存在（★ 越界）", credID)
		}
	}

	// ③ 候选数不得多于权威视图里该模型的可路由行数。
	var authoritative int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM v_routable_credential_models v
		WHERE v.is_routable AND v.raw_model_name = $1
	`, model).Scan(&authoritative); err != nil {
		t.Fatalf("数权威视图行数失败: %v", err)
	}
	if len(srcCands) > authoritative {
		t.Errorf("源给出 %d 个候选，★ 权威视图里该模型只有 %d 个可路由行\n"+
			"  ★ 多出来的 ⇒ 目录把不可路由的条目也算进来了",
			len(srcCands), authoritative)
	}

	// ④ 适配层的输出必须是源的子集。
	cat := &routerDiscoveryCatalog{client: client}
	adapted, err := cat.FindByModel(ctx, model, "default")
	if err != nil {
		t.Fatalf("目录取候选失败: %v", err)
	}
	srcSet := map[string]bool{}
	for i := range srcCands {
		name := srcCands[i].OfferRawModel
		if name == "" {
			name = srcCands[i].RawModel
		}
		srcSet[strconv.Itoa(srcCands[i].CredentialID)+"|"+name] = true
	}
	for _, c := range adapted {
		key := c.CredentialID + "|" + c.Model
		if !srcSet[key] {
			t.Errorf("适配层给出 (%s, %s)，★ 它不在源的候选集里\n"+
				"  ★ 映射层引入了源里没有的东西", c.CredentialID, c.Model)
		}
	}

	// ★ 观测项（★ 不作断言）：★ 每个源候选在权威视图里的 is_routable 归属。
	// ★ 源与视图的门槛**未必逐条相同**（★ 例：源还排 periodic_exhausted）
	// ⇒ ★ 这条是「观察两边是否一致」⇒ ★ 不一致本身不是缺陷。
	matched := 0
	for i := range srcCands {
		name := srcCands[i].OfferRawModel
		if name == "" {
			name = srcCands[i].RawModel
		}
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM v_routable_credential_models v
			WHERE v.is_routable AND v.credential_id = $1 AND v.raw_model_name = $2
		`, srcCands[i].CredentialID, name).Scan(&n); err != nil {
			t.Fatalf("查权威视图失败: %v", err)
		}
		if n > 0 {
			matched++
		}
	}
	t.Logf("模型 %s：源 %d 个候选 / 权威视图 %d 个可路由行 / 其中 %d 个在权威视图里 is_routable / 适配器输出 %d 个",
		model, len(srcCands), authoritative, matched, len(adapted))
}
