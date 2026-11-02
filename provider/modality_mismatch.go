package provider

import (
	"context"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// modalityMismatchQueryTimeout bounds the diagnosis query. Same budget and
// same rationale as streaming.alternativesQueryTimeout: a nicety on an
// already-failing request must never extend the client's wait.
const modalityMismatchQueryTimeout = 800 * time.Millisecond

// ModalityMismatch describes why a request found no candidate even though the
// model itself is known and routable.
//
// The zero-candidate exit has two very different causes that a client must be
// able to tell apart:
//
//   - the model has no usable provider at all right now (quota, probe state,
//     tenant scope) — retrying later, or switching model, are both sensible;
//   - the model IS routable, but it is registered as a text-only model while
//     this request carries images/audio — no amount of waiting helps, and the
//     only two real remedies are "send a text-only request" or "send this
//     request to a model that can read media".
//
// The second case is the one that used to be reported as a bare 503
// "No available provider for model 'X'", which reads as "the gateway is
// broken or busy". The client then retries the identical request forever,
// because nothing in the response says the request itself is the problem.
//
// ModalityMismatch carries the facts the error response needs to say that out
// loud: which modality the request carried, and which modality the model is
// registered as.
type ModalityMismatch struct {
	// RequestedModality is the modality detected from the request body
	// ("vision", "audio", "video", ...). Empty when the request was text-only.
	RequestedModality string
	// RegisteredModality is the modality the model is registered as in
	// models_canonical (usually "text").
	RegisteredModality string
	// CanonicalModel is the catalog name of the requested model, when known.
	CanonicalModel string
}

// IsZero reports whether no mismatch was detected.
func (m *ModalityMismatch) IsZero() bool {
	return m == nil || m.RequestedModality == ""
}

// ModelModalityMismatch reports whether the request's modality is what
// excluded the model from the candidate set.
//
// It answers the question by asking the database two things that must both
// hold:
//
//  1. the model IS routable when the modality filter is dropped — i.e. it has
//     a live (credential, model) binding, so the model itself is fine;
//  2. the model is registered as a modality that cannot serve the request.
//
// Only then is the zero-candidate exit a modality mismatch rather than a
// "nothing is routable" situation. When the model is not routable at all, the
// answer is "no candidates for now", which the caller must keep reporting as
// a retryable 503.
//
// The modality acceptance rule mirrors candidateQuerySQLBase's filter
// exactly — vision/audio/video are served by rows registered as that
// modality or as "multimodal" — so the diagnosis can never disagree with the
// gate that produced the zero candidates. The two must agree, or the error
// message would describe a cause the router never had.
func (c *Client) ModelModalityMismatch(ctx context.Context, model, profile, tenantID, requestModality string) (*ModalityMismatch, error) {
	if !c.Enabled() || c.dbPool == nil {
		return nil, nil
	}
	requestModality = strings.TrimSpace(strings.ToLower(requestModality))
	if !modalityDiagnosisQueryNeeded(requestModality) {
		// An empty request modality means "no modality filter" — nothing is
		// excluded by the gate, so there is nothing to diagnose.
		return nil, nil
	}
	// ★ 2026-11-02（211 生产事故 · embedding 投毒链）：文本请求**也会**被
	// 模态门排除——被排除的是登记为 'embedding' 的模型（chat 端点携带
	// embedding 模型 ⇒ 上游 400 ⇒ 绑定翻红 ⇒ /v1/embeddings 对所有租户 503）。
	// ⇒ 文本请求必须继续往下走（ModalityAccepts 的 text 臂已经表达了这条
	//   排除）：否则这类请求只会拿到一个**没有原因**的 503，客户端会无限重试
	//   ——而它重试的每一个字节都注定失败。
	//
	// ★ 成本：这条诊断是一次 800ms 有界的 DB 查询，它**只在零候选路径**上
	//   跑（见 domains/streaming/handler.go 的 diagnoseModalityMismatch）。
	//   2026-11-02（第八轮 · F-4 订正）：此前这段注释说「那里先用名字规则
	//   粗筛，普通文本模型不会触发这次查询」——**那个粗筛已经被删掉了**
	//   （2026-11-02 第三轮 d8bd4abb3：粗筛只贡献错误答案、不省时间，
	//   EXPLAIN ANALYZE 实测「查库粗筛 3.34ms vs 完整诊断 3.86ms」）⇒
	//   现在**每一次**零候选请求都会跑这次查询（文本请求也跑）。
	//   ⇒ 成本口径必须按「无条件」算，不能按「名字粗筛后才跑」算。
	//   ★ 真正的边界是**上游那道早退**：handler.go 的零候选路径先用
	//     `provider.ModelKnown` 挡掉「这个模型压根不在册」（⇒ 400
	//     invalid_model，不进本查询）⇒ 本查询只对「**在册但当下不可路由**」
	//     的模型跑。
	lookup := modelname.CanonicalizeClientModel(model)
	if lookup == "" {
		return nil, nil
	}

	// ★ Bounded like the alternatives lookup (800ms): this runs on a request
	// that has ALREADY failed candidate resolution. The diagnosis is an
	// improvement over the plain 503, never a reason to make a failing
	// client wait longer — a slow query here must degrade to the plain 503
	// (the caller swallows errors) rather than extend the failure latency.
	diagCtx, cancel := context.WithTimeout(ctx, modalityMismatchQueryTimeout)
	defer cancel()

	var (
		registeredModality       string
		canonicalModel           string
		routableIgnoringModality bool
	)
	err := c.dbPool.QueryRow(diagCtx, modalityMismatchSQL, lookup, tenantID).Scan(
		&registeredModality, &canonicalModel, &routableIgnoringModality)
	if err != nil {
		return nil, err
	}
	return modalityMismatchDecision(registeredModality, requestModality, canonicalModel, routableIgnoringModality), nil
}

// modalityDiagnosisQueryNeeded says whether the diagnosis query has to run
// for a request modality.
//
// ★ 它**只**回答「这次诊断值不值得查库」，不回答「是不是模态问题」
// （那是 modalityMismatchDecision 的事）——两条线必须分开：
// 一条线管成本，一条线管判决。
//
// ★ 唯一合法的早退是「请求没有模态概念」（模态为空 ⇒ 模态门没排除任何东西
// ⇒ 没有东西可诊断）。**文本请求必须查库**：文本臂恰恰**排除**登记为
// embedding 的模型（2026-11-02，211 生产事故 · embedding 投毒链）⇒
// chat + embedding 这一次零候选**就是**模态问题 ⇒ 不查库就永远给不出
// 400 modality_mismatch，客户端只会拿到一个没有原因的 503 然后无限重试。
//
// ★ 这条判据单独成为一函数，是为了让它**可单测**：2026-11-02 之前
// provider/modality_mismatch_test.go 里有一条门，它的注释断言
// 「文本请求从不被模态门排除，所以诊断连数据库都不该进」——
// ★★ 那个前提是**假的**（文本臂恰恰排除 embedding），而且那条门**没有真的
// 测早退**（它用的是 dbPool == nil 的 client，走的是另一道早退）
// ⇒ 「text 早退」这件事从头到尾没被验过，却有一条绿门站在那儿 ⇒
// 它会**引导**后来者去实现那个早退，而实现之后**它仍然是绿的**。
// ⇒ 现在这条判据被显式化并逐值钉死（provider/modality_mismatch_test.go
// 的 TestModalityDiagnosisQueryNeeded），任何「text 不查库」的改法当场红。
func modalityDiagnosisQueryNeeded(requestModality string) bool {
	return strings.TrimSpace(strings.ToLower(requestModality)) != ""
}

// modalityMismatchDecision 是「零候选是不是模态造成的」这条判决的**唯一实现**。
//
// ★ 为什么把它从 ModelModalityMismatch 里抽出来（2026-11-02 第十四轮 · F-16）：
//
// 判决的三个输入里，只有 registeredModality / routableIgnoringModality 需要查库。
// 判决**本身**是纯函数 ⇒ 它可以在**不接数据库**的情况下被逐格钉死。
// 在此之前它长在查询函数里，于是「判决表」只能靠真实数据库来验（realdb 测试，
// 没有 TEST_DATABASE_URL 时整条跳过）⇒ 本地永远验不到。
//
// ★ 2026-11-02 之前 provider/modality_mismatch_test.go 里有这样一条门：
// 「TestModelModalityMismatch_ShortCircuitsOnText」——
// 它的注释断言「文本请求从不被模态门排除，所以诊断连数据库都不该进」。
// ★★ 那个前提在 2026-11-02（211 生产事故 · embedding 投毒链）之后是**假的**：
// 文本臂恰恰**排除**登记为 embedding 的模型（ModalityAccepts 的 text 臂）。
// 而且这条门**并没有真的测早退**——它用的是 `&Client{}`（dbPool == nil），
// 走的是 `!c.Enabled() || c.dbPool == nil` 那道**另一个**早退，
// 于是「text 早退」这件事从头到尾没被验过，只是名字和注释在讲它。
// ⇒ 一条前提为假、且测的不是它自称的东西的门：它会**误导**后来者去实现
// 那个 text 早退（那会让 chat + embedding ⇒ 无原因的 503，事故链复活），
// 而且实现之后**它仍然是绿的**。
//
// ⇒ 抽出纯函数后，判决表可以在 provider 包内逐格钉死（不需要数据库），
// 「text 早退」这个已经被推翻的前提也有了明确的落点：判决表里写着
// (登记=embedding, 请求=text) ⇒ **有诊断**。
//
// ★ 判决的形状（与抽取前逐字一致）：
// 请求模态为空 ⇒ 无诊断（没有模态概念，没有东西可诊断）
// 忽略模态过滤后不可路由 ⇒ 无诊断（压根没绑定 ⇒ 是「暂时没东西可路由」）
// 判据说它能服务这个模态 ⇒ 无诊断（被别的原因排除：探针判决、租户范围…）
// 否则 ⇒ 模态不匹配（登记模态服务不了本次请求）
//
// ★ 注意第 2 条与第 3 条**顺序不能换**：先判「可路由」再判「能不能服务」，
// 否则一个压根没有绑定的模型会被说成「模态不对」——那是把客户端指向
// 一个它根本改不了的原因（客户端会改成纯文本请求，然后继续失败）。
func modalityMismatchDecision(registeredModality, requestModality, canonicalModel string, routableIgnoringModality bool) *ModalityMismatch {
	// 归一（与 ModelModalityMismatch 里那次同源、同处）：诊断返回给客户端的
	// RequestedModality 必须是归一后的令牌（"TEXT" / " text " ⇒ "text"）。
	requestModality = strings.TrimSpace(strings.ToLower(requestModality))
	if requestModality == "" {
		return nil
	}
	if !routableIgnoringModality {
		// The model has no live binding at all — nothing to do with modality.
		return nil
	}
	if ModalityAccepts(registeredModality, requestModality) {
		// The model could serve this modality; it was excluded for some other
		// reason (probe verdict, tenant scope, ...). Not a modality mismatch.
		return nil
	}
	return &ModalityMismatch{
		RequestedModality:  requestModality,
		RegisteredModality: registeredModality,
		CanonicalModel:     canonicalModel,
	}
}

// ★模态接受判据的实现**不在本文件**：它与判据的 SQL 形态同源，住在
// provider/modality_accept.go（ModalityAccepts + ModalityAcceptArmsSQL）。
// 本文件只负责「诊断」——用那条判据回答「这次零候选是不是模态造成的」。

// modalityMismatchSQL answers, in one round trip:
//
//	registered_modality — how the model is registered in models_canonical
//	canonical_model     — its catalog name
//	routable           — whether it has a live binding when the modality
//	                     filter is dropped
//
// The routable check deliberately reuses the candidate query's own name
// resolution (offers / aliases / canonical) so "the model is routable" means
// exactly what it means to the router. It differs from the router only in
// dropping the modality predicate — which is the whole point: the pair
// (routable=true, modality cannot serve) is the definition of the mismatch.
//
// v_routable_credential_models is the same availability gate the router uses
// (is_routable), so "routable here" and "routable for the router" cannot
// drift apart.
const modalityMismatchSQL = `
WITH matched AS MATERIALIZED (
    -- ★ The canonical comes from mc.id (COALESCE(mo.canonical_id, ma.canonical_id)),
    -- NOT from mo.canonical_id: an offer whose own canonical_id is NULL can
    -- still resolve through the alias join, and the router counts exactly
    -- those. Selecting mo.canonical_id here made every alias-resolved offer
    -- read as "unresolved" ⇒ the routable EXISTS came back false ⇒ the
    -- diagnosis stayed silent for models the router was happily excluding
    -- by modality (found in the 2026-10-28 critical audit; conservative
    -- miss, not a false positive — but the whole point of this query is to
    -- catch those).
    SELECT mc.id AS canonical_id, mo.canonical_raw_name, mo.standardized_name,
           mc.canonical_name, mc.modality,
           mo.id AS offer_id
    FROM model_offers mo
    LEFT JOIN model_name_mapping mnm
           ON mnm.raw_model_name = mo.canonical_raw_name
    LEFT JOIN model_aliases ma
           ON ma.raw_name = mo.canonical_raw_name
          AND COALESCE(ma.status, 'active') = 'active'
    LEFT JOIN models_canonical mc ON mc.id = COALESCE(mo.canonical_id, ma.canonical_id)
    WHERE (
          mo.canonical_raw_name = $1
          OR mo.standardized_name = $1
          OR mnm.standardized_name = $1
          OR EXISTS (
              SELECT 1 FROM model_aliases ma2
              WHERE ma2.raw_name = $1
                AND COALESCE(ma2.status, 'active') = 'active'
                AND (
                    (mo.canonical_id IS NOT NULL AND ma2.canonical_id = mo.canonical_id)
                    OR (mo.canonical_id IS NULL AND ma2.canonical_id IS NULL)
                )
          )
          OR lower(mc.canonical_name) = $1
      )
      AND COALESCE(mc.status, 'active') != 'disabled'
),
resolved AS (
    -- A request name can match offers bound to more than one canonical
    -- (the router then returns the union). The diagnosis only runs when the
    -- router returned ZERO candidates, which means every matched canonical
    -- failed the modality filter — so any single canonical answers the
    -- "what is it registered as" question; deterministic pick for stability.
    SELECT m.canonical_name, m.modality, m.canonical_id
    FROM matched m
    WHERE m.canonical_id IS NOT NULL
    ORDER BY m.canonical_name
    LIMIT 1
)
SELECT
    COALESCE(
        (SELECT r.modality FROM resolved r WHERE r.modality IS NOT NULL LIMIT 1),
        'text'
    ),
    COALESCE(
        (SELECT r.canonical_name FROM resolved r LIMIT 1),
        $1
    ),
    EXISTS (
        -- ★ Join by the OFFER's binding identity, NOT by pm.canonical_id:
        -- an alias-only offer's provider_models row has canonical_id = NULL —
        -- that is what "alias-only" means — so a canonical_id join drops
        -- exactly the rows this diagnosis exists to see. The test proved it
        -- twice: matched resolved the canonical correctly both times, and
        -- routable still came back false, because the EXISTS keyed on a
        -- column that alias-only offers leave NULL.
        --
        -- model_offers.id IS the credential_model_bindings id (the view is
        -- cmb ⋈ pm), so the direct route is the binding view itself:
        -- v_routable_credential_models carries binding_id /
        -- provider_model_id / credential_id and is the same availability
        -- gate the router consults.
        SELECT 1
        FROM v_routable_credential_models v
        WHERE v.binding_id IN (SELECT m.offer_id FROM matched m)
          AND v.is_routable = TRUE
          AND (v.tenant_id = $2 OR v.tenant_id = 'default')
    )
`
