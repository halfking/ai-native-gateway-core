// bg/modality_verification_test.go — 定时核实任务的判级逻辑
//
// 这组测试里 scan/persist/rollup 三个接缝都被注入，所以可以在没有网关
// schema 的环境里测「拿到一条判词之后到底写不写、写什么」这条决策链。
// probe 接缝在端到端用例里也注入成假上游，但 ProbeVisionSemantics 本身
// 的判级由 modality_semantic_probe_test.go 对着真实帧测。
package bg

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// readBGSource 读同目录的源文件，供「SQL 与 Go 两份实现必须同步」这类
// 结构性断言使用。
func readBGSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// stubDecrypt 让端到端用例不必造一个真密文信封：解密在本任务里是
// 接缝，判级逻辑与它无关。
func stubDecrypt(t modalityVerifyTarget) (string, error) { return "sk-test", nil }

// 核心判据：两胜定「真能读」、两负定「真看不见」，且 unknown 不计入任何
// 一侧的连击。
func TestApplyStreak(t *testing.T) {
	cases := []struct {
		name     string
		prev     string
		pos, neg int
		newLevel string
		wantLv   string
		wantPos  int
		wantNeg  int
	}{
		{
			name: "one pass is not yet confirmed", prev: ModalityLevelUnknown,
			pos: 0, neg: 0, newLevel: ModalityLevelConfirmed,
			wantLv: ModalityLevelUnknown, wantPos: 1, wantNeg: 0,
		},
		{
			name: "two passes confirm", prev: ModalityLevelUnknown,
			pos: 1, neg: 0, newLevel: ModalityLevelConfirmed,
			wantLv: ModalityLevelConfirmed, wantPos: 2, wantNeg: 0,
		},
		{
			name: "one wrong is not yet negative", prev: ModalityLevelUnknown,
			pos: 0, neg: 0, newLevel: ModalityLevelNegative,
			wantLv: ModalityLevelUnknown, wantPos: 0, wantNeg: 1,
		},
		{
			name: "two wrongs go negative", prev: ModalityLevelUnknown,
			pos: 0, neg: 1, newLevel: ModalityLevelNegative,
			wantLv: ModalityLevelNegative, wantPos: 0, wantNeg: 2,
		},
		{
			// 关键：已 confirmed 的模型再错一次，要退回未确认而不是
			// 永久停在 confirmed。
			name: "a pass after confirm resets", prev: ModalityLevelConfirmed,
			pos: 2, neg: 0, newLevel: ModalityLevelNegative,
			wantLv: ModalityLevelConfirmed, wantPos: 0, wantNeg: 1,
		},
		{
			// 更关键：inconclusive **不动任何计数**。把它算成一次失败，
			// 一个间歇性超时的模型会在两轮超时后被永久降级成 text。
			name: "inconclusive is not a failure", prev: ModalityLevelUnknown,
			pos: 0, neg: 1, newLevel: ModalityLevelUnknown,
			wantLv: ModalityLevelUnknown, wantPos: 0, wantNeg: 1,
		},
		{
			name: "inconclusive after confirm keeps confirm", prev: ModalityLevelConfirmed,
			pos: 2, neg: 0, newLevel: ModalityLevelUnknown,
			wantLv: ModalityLevelConfirmed, wantPos: 2, wantNeg: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lv, pos, neg := applyStreak(tc.prev, tc.pos, tc.neg, tc.newLevel)
			if lv != tc.wantLv || pos != tc.wantPos || neg != tc.wantNeg {
				t.Errorf("applyStreak(%q,%d,%d,%q) = (%q,%d,%d) want (%q,%d,%d)",
					tc.prev, tc.pos, tc.neg, tc.newLevel, lv, pos, neg,
					tc.wantLv, tc.wantPos, tc.wantNeg)
			}
		})
	}
}

func TestModalityVerifyAdmit(t *testing.T) {
	ok := modalityVerifyTarget{
		BindingAvailable: true, ProviderEnabled: true, LifecycleStatus: "active",
		CredentialStatus: "active", Modality: "vision",
	}
	cases := []struct {
		name    string
		mutate  func(*modalityVerifyTarget)
		wantOK  bool
		wantWhy string
	}{
		{"healthy", func(*modalityVerifyTarget) {}, true, ""},
		{"binding unavailable", func(t *modalityVerifyTarget) { t.BindingAvailable = false }, false, "binding_unavailable"},
		{"credential disabled", func(t *modalityVerifyTarget) { t.CredentialDisabled = true }, false, "credential_manual_disabled"},
		{"provider disabled", func(t *modalityVerifyTarget) { t.ProviderDisabled = true }, false, "provider_manual_disabled"},
		{"provider off", func(t *modalityVerifyTarget) { t.ProviderEnabled = false }, false, "provider_disabled"},
		{"lifecycle gone", func(t *modalityVerifyTarget) { t.LifecycleStatus = "retired" }, false, "lifecycle_retired"},
		{"credential quarantined", func(t *modalityVerifyTarget) { t.CredentialStatus = "quarantine" }, false, "credential_status_quarantine"},
		{"modality unresolved", func(t *modalityVerifyTarget) { t.Modality = "" }, false, "modality_unresolved"},
		{"modality nonsense", func(t *modalityVerifyTarget) { t.Modality = "hologram" }, false, "modality_hologram"},

		// ⚠ 2026-10-05 加的两个用例，钉的是**一个真实修掉的缺陷**。
		//
		// 本表初版对 audio/video 一律 wantOK=true，而当时唯一的探针实现
		// ProbeVisionSemantics 把探针模态写死成 "vision"、**不接受 modality
		// 参数**，生产接线也没把 t.Modality 传进去 ⇒ 放行 audio 的直接后果是
		// 给 ASR/TTS 发图像挑战，并把结果记成「audio 被拒」。
		// 真库实测有 12 绑定 / 8 个模型会走到这条路（gpt-audio、mimo-v2.5-asr、
		// mimo-v2.5-tts …），其中两个正是迁移 820 刚纠正成 audio 的例子。
		//
		// 这条用例同时是**恒真防护**：把两个 case 写成同一个 why 也发现不了
		// "放行"与"拒绝"被合并，所以 wantWhy 与 wantOK 都各自独立断言。
		{"audio has no probe yet", func(t *modalityVerifyTarget) { t.Modality = "audio" }, false, "modality_no_probe_audio"},
		{"video has no probe yet", func(t *modalityVerifyTarget) { t.Modality = "video" }, false, "modality_no_probe_video"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := ok
			tc.mutate(&target)
			gotOK, why := modalityVerifyAdmit(target)
			if gotOK != tc.wantOK {
				t.Fatalf("admit ok = %v want %v (why=%q)", gotOK, tc.wantOK, why)
			}
			if why != tc.wantWhy {
				t.Errorf("why = %q want %q", why, tc.wantWhy)
			}
		})
	}
}

// 本任务存在的头号理由：当前被标成 text 的模型也必须进探测队列。
// 现状 verifyTargetModality 在 t.Modality=="text" 时直接 return，
// 所以这条断言就是「升级发现能力」的回归锚。
func TestResolveModalityToProbeIncludesTextModels(t *testing.T) {
	cases := []struct {
		stored string
		model  string
		want   string
	}{
		// 关键用例：text 存储值必须去探 vision。
		{"text", "gpt-4o", "vision"},
		{"text", "glm-4.5v", "vision"},
		{"text", "qwen3-vl-plus", "vision"},
		// 存量 audio 行探 audio。
		{"audio", "whisper-1", "audio"},
		{"video", "some-video-model", "video"},
		// text 存储值但名字是 ASR 家族 ⇒ 探 audio（存量漏标的音频模型）。
		{"text", "mimo-v2.5-asr", "audio"},
		{"text", "qwen3-asr-0.6b", "audio"},
		{"text", "gpt-4o-transcribe", "audio"},
		// vision 存储值复核 vision。
		{"vision", "claude-sonnet-4", "vision"},
	}
	for _, tc := range cases {
		t.Run(tc.stored+"/"+tc.model, func(t *testing.T) {
			if got := resolveModalityToProbe(tc.stored, tc.model); got != tc.want {
				t.Errorf("resolveModalityToProbe(%q,%q) = %q want %q",
					tc.stored, tc.model, got, tc.want)
			}
		})
	}
}

// SQL 里那份模态决策与 Go 侧必须一致。两份实现漂移的后果是一行在两轮
// 之间来回换模态、永不停歇——它会一直占探测预算。
func TestModalitySQLMatchesGoRule(t *testing.T) {
	src := readBGSource(t, "modality_verification.go")
	for _, needle := range []string{
		"IN ('audio', 'video')",
		"(asr|tts|whisper|transcri|stt)",
		"ELSE 'vision'",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("scan SQL lost fragment %q —— Go 侧 resolveModalityToProbe "+
				"仍是同一份规则，两边必须同步改", needle)
		}
	}
	// audio 家族正则与 modelname.InferModality 的覆盖面有重叠但不完全
	// 相同（正则只是预筛）。这条钉住「预筛是超集」这个方向：正则认得的
	// 名字，规则表也得认。
	for _, name := range []string{"mimo-v2.5-asr", "qwen3-asr-0.6b", "gpt-4o-transcribe", "whisper-1"} {
		if got := resolveModalityToProbe("text", name); got != "audio" {
			t.Errorf("SQL 正则会选中 %q 探 audio，但 Go 侧给出 %q", name, got)
		}
	}
}

func TestRicherModality(t *testing.T) {
	cases := []struct{ stored, confirmed, want string }{
		{"text", "vision", "vision"},
		{"text", "audio", "audio"},
		{"vision", "vision", "vision"},
		{"audio", "vision", "multimodal"},
		{"vision", "audio", "multimodal"},
		{"multimodal", "vision", "multimodal"},
		{"multimodal", "audio", "multimodal"},
		{"vision", "multimodal", "multimodal"},
	}
	for _, tc := range cases {
		if got := richerModality(tc.stored, tc.confirmed); got != tc.want {
			t.Errorf("richerModality(%q,%q) = %q want %q", tc.stored, tc.confirmed, got, tc.want)
		}
	}
}

// 端到端：一个当前标成 text 的模型，探到两轮语义正证据后被升级成 vision。
// 这是整个目标的验收形态。
func TestVerifyOnce_UpgradesTextModelAfterTwoSemanticPasses(t *testing.T) {
	var mu sync.Mutex
	probes := 0

	target := modalityVerifyTarget{
		CredentialID: 7, CanonicalID: 42, CanonicalName: "glm-4.5v",
		RawModel: "glm-4.5v", OutboundModel: "glm-4.5v",
		Modality: "vision", StoredModality: "text",
		BaseURL: "https://example.invalid/v1", Protocol: "openai-completions",
		BindingAvailable: true, ProviderEnabled: true,
		LifecycleStatus: "active", CredentialStatus: "active",
	}

	v := &ModalityVerification{
		attempts:    map[string]time.Time{},
		dailyBudget: 100,
		batchLimit:  10,
	}
	v.decryptFn = stubDecrypt
	v.scan = func(context.Context) ([]modalityVerifyTarget, error) { return []modalityVerifyTarget{target}, nil }
	v.probe = func(context.Context, modalityVerifyTarget, string) SemanticProbeResult {
		mu.Lock()
		defer mu.Unlock()
		probes++
		return SemanticProbeResult{
			Carry:     ModalityLevelAccepted,
			Read:      ModalityLevelConfirmed,
			Expected:  []string{"red", "green", "blue", "yellow"},
			Mentioned: []string{"red", "green", "blue", "yellow"},
			Score:     4,
			Answer:    "red, green, blue, yellow",
		}
	}

	var persisted []modalityVerifyTarget
	var readLevels []string
	v.persist = func(_ context.Context, tt modalityVerifyTarget, res SemanticProbeResult) error {
		mu.Lock()
		defer mu.Unlock()
		// 复刻真实的连击口径：第 1 轮不该落 confirmed。
		lv, pos, neg := applyStreak(tt.ExistingRead, tt.PosStreak, tt.NegStreak, res.Read)
		tt.ExistingRead, tt.PosStreak, tt.NegStreak = lv, pos, neg
		persisted = append(persisted, tt)
		readLevels = append(readLevels, lv)
		return nil
	}
	var rolledUp int
	v.rollup = func(context.Context, modalityVerifyTarget) error { rolledUp++; return nil }

	// 第 1 轮：一次正证据还不够定论。
	if _, err := v.VerifyOnce(context.Background()); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if readLevels[0] != ModalityLevelUnknown {
		t.Errorf("round 1 read_level = %q want %q —— 单次判据的假阳性率是 1/1680，"+
			"不足以永久标多模态", readLevels[0], ModalityLevelUnknown)
	}

	// 第 2 轮：带上第 1 轮的连击进度（模拟 SQL 读回 streak），并清空
	// 退避台账——那代表「过了 6 小时」。退避本身由
	// TestVerifyOnce_AttemptLedgerBlocksRapidReprobe 单独钉住。
	target.ExistingRead = ModalityLevelUnknown
	target.PosStreak = 1
	v.attemptsMu.Lock()
	v.attempts = map[string]time.Time{}
	v.attemptsMu.Unlock()

	if _, err := v.VerifyOnce(context.Background()); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if readLevels[1] != ModalityLevelConfirmed {
		t.Fatalf("round 2 read_level = %q want %q", readLevels[1], ModalityLevelConfirmed)
	}
	// rollup 两轮都被调用：真正的「能不能改标注」闸门在 825 那个
	// v_model_modality_verdict 视图里（verdict='unknown' 时 no-op），
	// Go 这层不该、也没法自己判。
	if rolledUp != 2 {
		t.Errorf("rollup ran %d times, want 2 (one per persisted row)", rolledUp)
	}
	if probes != 2 {
		t.Errorf("probes = %d want 2", probes)
	}
}

// 负样本：没有证据（carry 与 read 都 unknown）时一行都不许写。
// 静默跳过会让「为什么这批模型还是没结论」变成不可查的问题，所以这条
// 与「必须写」的那条同样重要。
func TestVerifyOnce_NoEvidenceWritesNothing(t *testing.T) {
	target := modalityVerifyTarget{
		CredentialID: 9, CanonicalName: "gpt-4o", RawModel: "gpt-4o",
		Modality: "vision", StoredModality: "vision",
		BaseURL: "https://example.invalid/v1", Protocol: "openai-completions",
		BindingAvailable: true, ProviderEnabled: true,
		LifecycleStatus: "active", CredentialStatus: "active",
	}
	v := &ModalityVerification{attempts: map[string]time.Time{}, dailyBudget: 100, batchLimit: 10}
	v.decryptFn = stubDecrypt
	v.scan = func(context.Context) ([]modalityVerifyTarget, error) { return []modalityVerifyTarget{target}, nil }
	v.probe = func(context.Context, modalityVerifyTarget, string) SemanticProbeResult {
		return SemanticProbeResult{
			Carry: ModalityLevelUnknown, Read: ModalityLevelUnknown,
			ErrCode: "auth", ErrMsg: "401",
		}
	}
	wrote, rolled := false, false
	v.persist = func(context.Context, modalityVerifyTarget, SemanticProbeResult) error { wrote = true; return nil }
	v.rollup = func(context.Context, modalityVerifyTarget) error { rolled = true; return nil }

	n, err := v.VerifyOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("written = %d want 0", n)
	}
	if wrote {
		t.Error("persist ran without evidence —— 把「没探到」写成结论等于用默认值关掉一个正常绑定")
	}
	if rolled {
		t.Error("rollup ran without evidence")
	}
}

// 负样本：admission 拒绝的行不出网、不写、不进退避台账。
func TestVerifyOnce_AdmissionRejectedIsFree(t *testing.T) {
	target := modalityVerifyTarget{
		CredentialID: 11, RawModel: "m", Modality: "vision",
		BindingAvailable: false, // 唯一关掉的闸门
	}
	v := &ModalityVerification{attempts: map[string]time.Time{}, dailyBudget: 100, batchLimit: 10}
	v.decryptFn = stubDecrypt
	v.scan = func(context.Context) ([]modalityVerifyTarget, error) { return []modalityVerifyTarget{target}, nil }
	probed := false
	v.probe = func(context.Context, modalityVerifyTarget, string) SemanticProbeResult {
		probed = true
		return SemanticProbeResult{}
	}
	v.persist = func(context.Context, modalityVerifyTarget, SemanticProbeResult) error { return nil }

	if _, err := v.VerifyOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probed {
		t.Error("probe ran for an admission-rejected target")
	}
	if v.attemptedRecently(target) {
		t.Error("admission-rejected target entered the attempt ledger —— 零出网成本不该被退避")
	}
	if got := v.budgetRemaining(time.Now()); got != 100 {
		t.Errorf("budget consumed by a zero-egress target: remaining = %d want 100", got)
	}
}

// 负样本：预算触顶后不再出网。
func TestVerifyOnce_BudgetExhaustedStopsProbing(t *testing.T) {
	newTarget := func(id int) modalityVerifyTarget {
		return modalityVerifyTarget{
			CredentialID: id, RawModel: "m", Modality: "vision",
			BindingAvailable: true, ProviderEnabled: true,
			LifecycleStatus: "active", CredentialStatus: "active",
		}
	}
	targets := []modalityVerifyTarget{newTarget(1), newTarget(2), newTarget(3)}
	v := &ModalityVerification{attempts: map[string]time.Time{}, dailyBudget: 1, batchLimit: 10}
	v.decryptFn = stubDecrypt
	v.scan = func(context.Context) ([]modalityVerifyTarget, error) { return targets, nil }
	probes := 0
	v.probe = func(context.Context, modalityVerifyTarget, string) SemanticProbeResult {
		probes++
		return SemanticProbeResult{
			Carry: ModalityLevelAccepted, Read: ModalityLevelConfirmed,
			Expected: []string{"red"}, Mentioned: []string{"red"}, Score: 1,
		}
	}
	v.persist = func(context.Context, modalityVerifyTarget, SemanticProbeResult) error { return nil }
	v.rollup = func(context.Context, modalityVerifyTarget) error { return nil }

	if _, err := v.VerifyOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Errorf("probes = %d want 1 —— 日预算 1 就是只能出网一次", probes)
	}
}

// 负样本：退避台账让近期探过的行不重复花钱。
func TestVerifyOnce_AttemptLedgerBlocksRapidReprobe(t *testing.T) {
	target := modalityVerifyTarget{
		CredentialID: 21, RawModel: "m", Modality: "vision",
		BindingAvailable: true, ProviderEnabled: true,
		LifecycleStatus: "active", CredentialStatus: "active",
	}
	v := &ModalityVerification{attempts: map[string]time.Time{}, dailyBudget: 100, batchLimit: 10}
	v.decryptFn = stubDecrypt
	v.scan = func(context.Context) ([]modalityVerifyTarget, error) { return []modalityVerifyTarget{target}, nil }
	probes := 0
	v.probe = func(context.Context, modalityVerifyTarget, string) SemanticProbeResult {
		probes++
		return SemanticProbeResult{Carry: ModalityLevelRejected, Read: ModalityLevelUnknown}
	}
	v.persist = func(context.Context, modalityVerifyTarget, SemanticProbeResult) error { return nil }

	for i := 0; i < 3; i++ {
		if _, err := v.VerifyOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if probes != 1 {
		t.Errorf("probes = %d want 1 —— 退避台账没起作用", probes)
	}
}

func TestModalityVerifyKillSwitch(t *testing.T) {
	t.Setenv(ModalityVerifyEnvKillSwitch, "0")
	if modalityVerifyEnabled() {
		t.Error("kill switch '0' did not disable the task")
	}
	t.Setenv(ModalityVerifyEnvKillSwitch, "off")
	if modalityVerifyEnabled() {
		t.Error("kill switch 'off' did not disable the task")
	}
	t.Setenv(ModalityVerifyEnvKillSwitch, "")
	if !modalityVerifyEnabled() {
		t.Error("empty kill switch should leave the task enabled (opt-out, not opt-in)")
	}
}

func TestModalityVerifyBudgetEnv(t *testing.T) {
	old, had := os.LookupEnv(modalityVerifyBudgetEnv)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(modalityVerifyBudgetEnv, old)
		} else {
			_ = os.Unsetenv(modalityVerifyBudgetEnv)
		}
	})
	if got := modalityVerifyDailyBudget(); got != modalityVerifyDefaultDailyBudget {
		t.Errorf("unset budget = %d want %d", got, modalityVerifyDefaultDailyBudget)
	}
	_ = os.Setenv(modalityVerifyBudgetEnv, "not-a-number")
	if got := modalityVerifyDailyBudget(); got != modalityVerifyDefaultDailyBudget {
		t.Errorf("garbage budget = %d want default %d", got, modalityVerifyDefaultDailyBudget)
	}
	_ = os.Setenv(modalityVerifyBudgetEnv, "17")
	if got := modalityVerifyDailyBudget(); got != 17 {
		t.Errorf("budget = %d want 17", got)
	}
}

// modality 证据列参数守卫（2026-10-05 R24 审计根修）。
//
// persistRow 的 $10/$11 与 rollupVerdict 的 $2（carry_evidence/read_evidence/
// modality_evidence）不允许裸 []byte 进 Exec：SimpleProtocol 会把它内联为
// bytea hex 字面量（'\x7b22…'），jsonb 解析必炸 —— 252 真库 4.4h 窗口 6 次
// 失败，modality 判级与 canonical 回写一并丢失。与 capability_backfill 的
// evidence_json 同根（R11 FIX-C / R22 第四断点家族）。判据钉在 AST 上不钉
// 在源码子串上：注释里就写着「evidence」，子串门会被注释自己喂饱。
// 变异验证：把任一处改回裸 evidence → 本用例红。
func TestModalityRowEvidenceParamGuard(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modality_verification.go", nil, 0)
	if err != nil {
		t.Fatalf("parse modality_verification.go: %v", err)
	}
	bare := map[string]bool{"carryEvidence": true, "readEvidence": true, "evidence": true}
	var offenders []string
	//nolint:staticcheck // 包内相对路径，bg 包 AST 守卫惯例
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Exec" {
			return true
		}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && bare[id.Name] {
				offenders = append(offenders, fset.Position(arg.Pos()).String())
			}
		}
		return true
	})
	if len(offenders) > 0 {
		t.Fatalf("Exec 直接传裸 []byte 证据参数于 %v；必须经 capabilityEvidenceParam "+
			"（SimpleProtocol 会把 []byte 内联为 bytea hex 字面量，jsonb 解析必炸）", offenders)
	}
}
