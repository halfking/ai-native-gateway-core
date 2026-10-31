package provider

import "strings"

// 模态接受判据的【唯一实现】。本文件是判据的唯一定义处：
//
//	③ Go 判据：ModalityAccepts（诊断用；决定「这是不是模态不匹配」）
//	①② SQL 片段：ModalityAcceptArmsSQL（路由器候选集 + 零候选建议列表共用）
//
// ★ 为什么必须只有一份（2026-10-29 批判式审计第二轮 §8 登记的「五份表达统一
// 锁步」工程）：此前仓里有五份表达——① provider.candidateQuerySQLBase 的 $3
// 接受臂、② domains/streaming 的 alternativesSQL 的 $5 过滤块、③
// provider.ModalityAccepts、④ supportedModalitiesFor（错误载荷的发布表）、
// ⑤ 错误载荷的 supported_modalities 字段。前两轮把它做成了「改一份忘另一份
// 会当场红」（三道锁步门），但**抄本仍是抄本** ⇒第五份、第六份（注释）照旧
// 会各自漂。
// ⇒ 本轮把它收成**只有一份**：两份 SQL 抄本从同一个片段渲染，Go 判据是判据
// 本身，发布表由 Go 判据派生。
//
// ★ 判据的形态（登记模态 R × 请求模态 P，R 为空按 'text' 归一）：
//
//	P = ''            →放行（无模态过滤）
//	P = 'text'        → 放行
//	P ∈ {vision,audio,video} → R ∈ {P, 'multimodal'}
//	P = 'multimodal'  → R = 'multimodal'
//	P = 'image'       → R ∈ {'image', 'vision', 'multimodal'}
//	其它 P（未知模态）→ R = P（**精确相等**）
//
// ★★ 未知模态那一格为什么是「精确相等」而不是「R = P 或 R = 'multimodal'」：
// 这不是审美选择，是**跟着生产事实源走**——路由器的候选集查询（①）的兜底臂
// 就是 `COALESCE(mc.modality,'text') = $P`（精确），诊断判据必须与它一致，
// 否则诊断会说出「路由器从来没有的那个原因」（LESSONS §531 通用式四：
// 逐处比对；且 §559通用式一：域外语义也必须一致）。
// ⇒ ★ **兜底精确臂是承重的**：/v1/embeddings 路由用 $P='embedding'，
//
//	靠它命中登记为 'embedding' 的行。若把它放宽成「multimodal 也接」，
//	 嵌入请求会被路由到 chat 型多模态模型 ⇒ 那是**功能事故**，不是风格问题。
//
// ⇒ ★ 而「登记为 multimodal 的模型接不接一个未来的 'document' 请求」这件事，
//
//	由**兜底臂的精确语义**决定（不接）⇒ 若将来要接，必须先改这条判据本身
//	（三份表达同源 ⇒ 改一处即可），而不是让 Go 侧单独放宽。
func ModalityAccepts(registeredModality, requestedModality string) bool {
	registeredModality = strings.TrimSpace(strings.ToLower(registeredModality))
	if registeredModality == "" {
		registeredModality = "text"
	}
	requestedModality = strings.TrimSpace(strings.ToLower(requestedModality))
	switch requestedModality {
	case "":
		// An empty request modality means "no modality filter" — the router
		// admits everything in that case ($3 = '' in the acceptance arms).
		// Treating it as an unknown media modality would report a mismatch
		// for a request the router never filtered.
		return true
	case "text":
		return true
	case "multimodal":
		return registeredModality == "multimodal"
	case "image":
		return registeredModality == "image" || registeredModality == "vision" || registeredModality == "multimodal"
	case "vision", "audio", "video":
		// 媒体请求：同模态登记，或 multimodal 登记（multimodal = text+image+audio+video，
		// 见 modelname/modality_defaults.go）。
		return registeredModality == requestedModality || registeredModality == "multimodal"
	default:
		// ★ 未知请求模态 ⇒ **精确相等**（与 SQL 兜底臂 `COALESCE(mc.modality,'text') = $P`
		// 同形）。此前这里是 `registered == requested || registered == "multimodal"`
		// （宽解释）⇒ 与两份 SQL 抄本在 (multimodal, 未知值) 格**语义相反**：
		// SQL 判 false（不接纳）、Go 判 true（可接纳）⇒ 诊断会对一个路由器
		// 压根没接纳的模型说「这不是模态问题」⇒ 错误原因被隐藏。
		// ⇒ 收窄为精确相等 ⇒ ①②③ 三份表达在**全部**格上同义（不只是对拍域内）。
		return registeredModality == requestedModality
	}
}

// ModalityRequestModalities lists the request modalities the gateway can carry,
// in the order they are published to clients.
//
// ★ 事实依据（两条，缺一不可）：
//
//	· 登记模态的词表 = models_canonical.modality 的 CHECK 约束
//	  （text/vision/audio/video/multimodal/embedding）—— 由
//	  tests/testutil.CanonicalModalityValueDomain 从迁移 451 派生。
//	  「embedding」既是登记值，也是请求值（/v1/embeddings 端点传的就是它）。
//	· 「image」是**只出现在请求侧**的模态（/v1/images/* 专用端点声明它），
//	  不在登记词表里（admin validModalities 与 DB CHECK 都���有 image）。
//
// ⇒ 请求词表 =登记词表 ∪ {image}。
//
// ★ 门：domains/streaming/modality_single_source_test.go 的
// TestModalityRequestUniverse_AnchoredToFactSource 双向锚定它——
// 事实源加了值 ⇒ 门要求扩词表；词表里多出事实源没有的值 ⇒ 门红。
// （LESSONS §559 通用式二：分母必须锚在事实源上，否则「缩分母 + 改判据」
// 可以合谋出假绿。）
var ModalityRequestModalities = []string{
	"text", "vision", "audio", "video", "image", "multimodal", "embedding",
}

// modalityAcceptArmsTemplate 是模态接受判据的 **SQL 形态唯一来源**。
//
// ★形态（$MODALITY 是占位符 token，渲染时替换成调用方的参数号）：
//
//	$MODALITY = ''
//	OR $MODALITY = 'text'
//	OR ($MODALITY IN ('vision', 'audio', 'video') AND COALESCE(mc.modality, 'text') IN ($MODALITY, 'multimodal'))
//	OR ($MODALITY = 'multimodal' AND COALESCE(mc.modality, 'text') = 'multimodal')
//	OR ($MODALITY = 'image' AND COALESCE(mc.modality, 'text') IN ('image', 'vision', 'multimodal'))
//	OR COALESCE(mc.modality, 'text') = $MODALITY
//
// ★ 这段 SQL 与上面的 Go 判据是**同一份规则的两个投影**，必须永远同义。
//
//	两道锁步门（provider/modality_accepts_lockstep_test.go 的①↔③、
//	domains/streaming/model_alternatives_modality_lockstep_test.go 的 ②↔③）
//	逐格对拍；另有一道「只有一份」门
//	（domains/streaming/modality_single_source_test.go）钉住两份 SQL 抄本
//	都**由本片段渲染**、且各自**不含第二份抄本**。
//
// ★★ 为什么用「片段 + 占位符替换」而不是把 SQL 抄本直接拼接进主 SQL：
//
//	（a）**拼接会让 sql_comment_syntax_test.go 的扫描器失明**（实测，见
//	    provider/sql_comment_syntax_test.go 的 TestSQLCommentScanner_SeesPastConcatenation）：
//	    扫描器按「一行只有一个反引号 = 块的开头 / 块内的第一个反引号 = 块的结尾」
//	    配对 ⇒ 一行里有**两个**反引号（`... AND (` + frag + ` ` 这种拆法）时，
//	    它在**第一个**反引号处就把块关掉 ⇒ **块之后的 SQL整段不再被扫** ⇒
//	    那一段里的 `//` 注释对门**完全隐形**。
//	    ⇒ 所以主 SQL 必须**保持一个完整的 raw string**，用占位符 + strings.Replace
//	    替换（仓内既有先例：/*MODALITY_VERDICT_GATE*/）。
//	（b）**可读性**：判据在一处 ⇒ 改判据时只有一处可改。
//
// ★ 缩进用空格（不是 tab）：片段被嵌进两处缩进不同的上下文（provider 的
//
//	raw string 用 tab，streaming 的用空格）⇒ 片段自带中性缩进，两边都好读。
const modalityAcceptArmsTemplate = `
    $MODALITY = ''
    OR $MODALITY = 'text'
    -- 2026-10-26：vision/audio/video 三种媒体请求都放行
    -- multimodal 登记的模型。video 此前缺席（只有精确
    -- mc.modality = 'video' 一条臂）⇒ 任何带视频块的
    -- Gemini/Qwen-VL 请求对全部 multimodal 模型 503，
    -- 而判定门（modalityVerdictGateSQL）早已把 video 与
    -- vision/audio 同列 —— 迁移 451 把 video 加进枚举的
    -- 理由正是「modality_detect 能返回 video」。
    OR ($MODALITY IN ('vision', 'audio', 'video') AND COALESCE(mc.modality, 'text') IN ($MODALITY, 'multimodal'))
    -- 'multimodal' 请求（一次带多种模态的请求）⇒ 只接登记为 multimodal 的行。
    OR ($MODALITY = 'multimodal' AND COALESCE(mc.modality, 'text') = 'multimodal')
    -- 2026-10-08 Phase C: dedicated /v1/images/* uses modality=image
    -- and accepts vision|image|multimodal catalog rows.
    OR ($MODALITY = 'image' AND COALESCE(mc.modality, 'text') IN ('image', 'vision', 'multimodal'))
    --兜底精确臂：未知请求模态 ⇒ 只接登记为同一模态的行。
    -- ★ 承重：/v1/embeddings 靠它命中登记为 'embedding' 的行（见 ModalityAccepts
    --   的注释：放宽成「multimodal 也接」会把嵌入请求路由到 chat 模型）。
    OR COALESCE(mc.modality, 'text') = $MODALITY`

// ModalityAcceptArmsSQL renders the shared modality-acceptance arms for a
// given SQL placeholder.
//
// placeholder is the query parameter that carries the request modality:
// provider's candidate query uses "$3"; the alternatives (zero-candidate
// suggestion) query uses "$5".
//
// ★ 返回值是一段**可直接嵌进 WHERE 的布尔表达式**（不含外层括号）⇒ 两个调用点
//
//	各自决定要不要包一层括号。
func ModalityAcceptArmsSQL(placeholder string) string {
	if placeholder == "" {
		// 空占位符会让渲染结果变成 `$MODALITY`（一个未定义的参数）⇒ 宁可
		// 显式炸掉，也不要产出一条语法合法但语义错误的 SQL。
		panic("provider: ModalityAcceptArmsSQL 需要一个占位符（如 \"$3\"）")
	}
	return strings.ReplaceAll(modalityAcceptArmsTemplate, "$MODALITY", placeholder)
}
