package sessionmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── DetectExpertFromSystemPrompt: default registry ───

func TestDetectExpertFromSystemPrompt_AgentTypes(t *testing.T) {
	// The 13 agents in telemetry.request_metadata's defaultAgentPatterns whose
	// system prompt explicitly self-describes as a software-engineering role
	// ("coding", "engineering", "developer", "code") classify as
	// software_engineering here. Pure IDE prompts without engineering
	// vocabulary (zed, vscode) return "unknown" — and that's correct: the
	// system prompt never claims an expert specialty.
	cases := []struct {
		name     string
		system   string
		wantType string
	}{
		{"cursor composer", "You are an AI coding assistant, powered by Composer. You operate in Cursor.", ExpertSoftwareEngineering},
		{"zcode", "You are ZCode, an interactive coding agent. You are an agent for ZCode CLI.", ExpertSoftwareEngineering},
		{"opencode se", "You are opencode, an interactive CLI tool that helps users with software engineering tasks.", ExpertSoftwareEngineering},
		{"claude-code", "You are Claude Code, Anthropic's official CLI for software engineering.", ExpertSoftwareEngineering},
		{"codex", "You are an AI assistant for codex CLI, specialized in code generation.", ExpertSoftwareEngineering},
		{"roo-code", "You are roo code, an AI-powered autocompletion tool.", ExpertUnknown},
		{"windsurf", "You are windsurf, an AI coding assistant from Codeium.", ExpertSoftwareEngineering},
		{"copilot", "You are GitHub Copilot, the AI pair programmer.", ExpertSoftwareEngineering},
		{"cline", "You are Cline, an AI coding assistant for VSCode.", ExpertSoftwareEngineering},
		{"aider", "You are aider chat, the AI coding assistant.", ExpertSoftwareEngineering},
		{"continue", "You are continue, the open-source AI code assistant.", ExpertSoftwareEngineering},
		{"kiro", "You are kiro, an AI-powered IDE.", ExpertUnknown},
		{"zed", "You are zed, an editor that integrates with LLMs.", ExpertUnknown},
		{"vscode", "Visual Studio Code is your environment.", ExpertUnknown},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectExpertFromSystemPrompt(tt.system); got != tt.wantType {
				t.Fatalf("DetectExpertFromSystemPrompt(%q) = %q, want %q", tt.system, got, tt.wantType)
			}
		})
	}
}

func TestDetectExpertFromSystemPrompt_SpecialtyTypes(t *testing.T) {
	cases := []struct {
		name     string
		system   string
		wantType string
	}{
		{"data_scientist", "You are a data scientist working with Python and pandas.", ExpertDataScience},
		{"ml_engineer", "You are an ML engineer specializing in PyTorch and HuggingFace.", ExpertDataScience},
		{"security_researcher", "You are a senior security researcher analyzing vulnerability reports.", ExpertSecurity},
		{"security_engineer", "You are a security engineer focused on application security.", ExpertSecurity},
		{"pentest", "You are a penetration tester authorized to test web applications.", ExpertSecurity},
		{"devops", "You are a DevOps engineer responsible for CI/CD pipelines.", ExpertDevOps},
		{"sre", "You are an SRE managing production reliability.", ExpertDevOps},
		{"qa_engineer", "You are a QA engineer running test automation.", ExpertTesting},
		{"sdet", "You are an SDET building automated test frameworks.", ExpertTesting},
		{"ui_designer", "You are a UI designer reviewing mockups in Figma.", ExpertDesign},
		{"ux_designer", "You are a UX designer focused on user research.", ExpertDesign},
		{"researcher", "You are an academic researcher in machine learning.", ExpertResearch},
		{"product_manager", "You are a product manager writing PRDs.", ExpertProductManagement},
		{"tech_writer", "You are a technical writer producing API documentation.", ExpertDocumentation},
		{"support_agent", "You are a customer support agent handling tickets.", ExpertCustomerSupport},
		{"financial_analyst", "You are a financial analyst preparing quarterly reports.", ExpertFinance},
		{"lawyer", "You are a lawyer reviewing contracts.", ExpertLegal},
		{"marketing", "You are a marketing manager writing ad copy.", ExpertMarketing},
		{"translator", "You are a professional translator between English and Chinese.", ExpertTranslation},
		{"general", "You are a helpful assistant.", ExpertGeneralPurpose},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectExpertFromSystemPrompt(tt.system); got != tt.wantType {
				t.Fatalf("DetectExpertFromSystemPrompt(%q) = %q, want %q", tt.system, got, tt.wantType)
			}
		})
	}
}

// ─── Priority: specific roles must beat catch-all software_engineering ───

func TestDetectExpertFromSystemPrompt_SpecificBeatsCatchAll(t *testing.T) {
	// "you are an engineer" alone → software_engineering
	// "you are a security engineer" → security (must win over software_engineering)
	cases := []struct {
		name     string
		system   string
		wantType string
	}{
		{"engineer_only", "You are an engineer at Acme Corp.", ExpertSoftwareEngineering},
		{"security_engineer", "You are a security engineer auditing code for vulnerabilities.", ExpertSecurity},
		{"devops_engineer", "You are a DevOps engineer building deployment pipelines.", ExpertDevOps},
		{"ml_engineer", "You are an ML engineer training deep learning models.", ExpertDataScience},
		{"qa_engineer", "You are a QA engineer running automated tests.", ExpertTesting},
		{"data_scientist", "You are a data scientist analyzing user behavior.", ExpertDataScience},
		{"ui_engineer", "You are a UI engineer building design systems.", ExpertDesign},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectExpertFromSystemPrompt(tt.system); got != tt.wantType {
				t.Fatalf("got %q, want %q (specific role must beat catch-all software_engineering)", got, tt.wantType)
			}
		})
	}
}

// ─── Chaining: order in registry matters ───

func TestDetectExpertFromSystemPrompt_RegistryOrder(t *testing.T) {
	// Manually craft: a sentence contains both "you are an engineer" (catch-all)
	// and "you are a security engineer" (specific). Verify security wins.
	system := "You are a security engineer. As an engineer you also write code daily."
	got := DetectExpertFromSystemPrompt(system)
	if got != ExpertSecurity {
		t.Fatalf("expected security (specific) to beat software_engineering (catch-all); got %q", got)
	}
}

// ─── Edge cases ───

func TestDetectExpertFromSystemPrompt_Empty(t *testing.T) {
	if got := DetectExpertFromSystemPrompt(""); got != ExpertUnknown {
		t.Fatalf("empty system prompt must return %q, got %q", ExpertUnknown, got)
	}
}

func TestDetectExpertFromSystemPrompt_NoMatch(t *testing.T) {
	got := DetectExpertFromSystemPrompt("random unrelated text about cooking recipes")
	if got != ExpertUnknown {
		t.Fatalf("no-match prompt must return %q, got %q", ExpertUnknown, got)
	}
}

func TestDetectExpertFromSystemPrompt_CaseInsensitive(t *testing.T) {
	if got := DetectExpertFromSystemPrompt("YOU ARE A DATA SCIENTIST"); got != ExpertDataScience {
		t.Fatalf("uppercase must match; got %q", got)
	}
	if got := DetectExpertFromSystemPrompt("you ARE a Data Scientist"); got != ExpertDataScience {
		t.Fatalf("mixed case must match; got %q", got)
	}
}

func TestDetectExpertFromSystemPrompt_CJK(t *testing.T) {
	cases := []string{
		"你是一名数据科学家，使用 Python 进行分析。",
		"你是一个软件工程师，负责后端开发。",
		"你是一名安全研究员，专攻渗透测试。",
		"你是一名产品经理，负责需求评审。",
		"你是一名翻译，专注于中英互译。",
		"你是一名通用助手。",
	}
	want := []string{ExpertDataScience, ExpertSoftwareEngineering, ExpertSecurity, ExpertProductManagement, ExpertTranslation, ExpertGeneralPurpose}
	for i, sys := range cases {
		if got := DetectExpertFromSystemPrompt(sys); got != want[i] {
			t.Fatalf("CJK[%d] %q: got %q, want %q", i, sys, got, want[i])
		}
	}
}

// ─── Registry extensibility ───

func TestRegisterExpertPattern_Appends(t *testing.T) {
	defer ResetExpertPatterns()
	RegisterExpertPattern("blockchain", "blockchain engineer", "smart contract developer")
	if got := DetectExpertFromSystemPrompt("You are a blockchain engineer building dApps."); got != "blockchain" {
		t.Fatalf("registered pattern not detected; got %q", got)
	}
	// Default registry must still work
	if got := DetectExpertFromSystemPrompt("You are a data scientist."); got != ExpertDataScience {
		t.Fatalf("default registry broken after RegisterExpertPattern; got %q", got)
	}
}

func TestRegisterExpertPattern_EmptyInputsAreNoOp(t *testing.T) {
	defer ResetExpertPatterns()
	RegisterExpertPattern("", "ignored")          // empty type → ignored
	RegisterExpertPattern("ignored", "")          // empty patterns → ignored
	RegisterExpertPattern("ignored", "   ", "\t") // whitespace-only → ignored
	// No spurious patterns were added: the data scientist detection
	// should still hit the default data_science entry directly.
	if got := DetectExpertFromSystemPrompt("You are a data scientist."); got != ExpertDataScience {
		t.Fatalf("default registry broken after no-op RegisterExpertPattern; got %q", got)
	}
}

func TestResetExpertPatterns_RestoresDefaults(t *testing.T) {
	defer ResetExpertPatterns()
	RegisterExpertPattern("blockchain", "blockchain engineer")
	ResetExpertPatterns()
	// After reset the custom pattern is gone, so the prompt falls through to
	// the default catch-all software_engineering ("you are an engineer").
	if got := DetectExpertFromSystemPrompt("You are a blockchain engineer."); got != ExpertSoftwareEngineering {
		t.Fatalf("custom pattern must be cleared; got %q, want catch-all software_engineering", got)
	}
	// Defaults restored
	if got := DetectExpertFromSystemPrompt("You are a data scientist."); got != ExpertDataScience {
		t.Fatalf("defaults not restored; got %q", got)
	}
}

// ─── extractExpert: integration through Extract() ───

func TestExtract_PopulatesExpertFromSystemPrompt(t *testing.T) {
	got := Extract(Input{Messages: []Message{
		{Role: "system", Content: "You are a security researcher auditing code."},
		{Role: "user", Content: "find vulns"},
	}})
	if got.Expert.Type != ExpertSecurity {
		t.Fatalf("expert.type = %q, want %q", got.Expert.Type, ExpertSecurity)
	}
	if got.Expert.Source != "system_prompt" {
		t.Fatalf("expert.source = %q, want system_prompt", got.Expert.Source)
	}
	if got.Expert.Confidence <= 0 || got.Expert.Confidence > 1 {
		t.Fatalf("expert.confidence out of [0,1]: %v", got.Expert.Confidence)
	}
}

func TestExtract_ExpertExplicitOverridesSystemPrompt(t *testing.T) {
	got := Extract(Input{
		Expert:   ExpertFinance,
		Messages: []Message{{Role: "system", Content: "You are a data scientist."}, {Role: "user", Content: "x"}},
	})
	if got.Expert.Type != ExpertFinance || got.Expert.Source != "header" {
		t.Fatalf("explicit expert must win; got %+v", got.Expert)
	}
}

func TestExtract_ExpertUnknownWhenNoSignal(t *testing.T) {
	got := Extract(Input{Messages: []Message{{Role: "user", Content: "hello"}}})
	if got.Expert.Type != ExpertUnknown || got.Expert.Source != "unknown" {
		t.Fatalf("expert must be unknown; got %+v", got.Expert)
	}
}

// ─── JSON contract ───

func TestExtract_ExpertJSONContract(t *testing.T) {
	got := Extract(Input{Messages: []Message{
		{Role: "system", Content: "You are ZCode, an interactive coding agent."},
		{Role: "user", Content: "deploy"},
	}})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(encoded)
	if !strings.Contains(s, `"expert"`) {
		t.Fatalf("JSON missing expert field: %s", s)
	}
	if !strings.Contains(s, `"type":"software_engineering"`) {
		t.Fatalf("JSON missing software_engineering: %s", s)
	}
}

func TestExtract_ExpertAlwaysEmitted(t *testing.T) {
	// Go's struct omitempty is a no-op (zero-value structs serialize).
	// Expert is consistent with Agent / Client: always emitted, with
	// type="unknown" when no signal is detected.
	got := Extract(Input{Messages: []Message{{Role: "user", Content: "hello"}}})
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(encoded)
	if !strings.Contains(s, `"expert"`) {
		t.Fatalf("expert field must always be present (consistent with agent/client): %s", s)
	}
	if !strings.Contains(s, `"type":"unknown"`) {
		t.Fatalf("expert.type must be unknown when no signal: %s", s)
	}
}

// ─── Cross-check: Cursor / ZCode / opencode all classify as software_engineering ───

func TestExtract_UserMentionedAgents_AllSoftwareEngineering(t *testing.T) {
	// The user's request: identify agent + expert from system prompt signature.
	cases := []struct {
		agentName string
		system    string
	}{
		{"cursor", "You are an AI coding assistant, powered by Composer. You operate in Cursor."},
		{"zcode", "You are ZCode, an interactive coding agent\nYou are an agent for ZCode CLI. "},
		{"opencode", "You are opencode, an interactive CLI tool that helps users with software engineering tasks. "},
	}
	for _, tt := range cases {
		t.Run(tt.agentName, func(t *testing.T) {
			got := Extract(Input{Messages: []Message{
				{Role: "system", Content: tt.system},
				{Role: "user", Content: "test prompt"},
			}})
			if got.Agent.Name != tt.agentName {
				t.Fatalf("agent.name = %q, want %q", got.Agent.Name, tt.agentName)
			}
			if got.Expert.Type != ExpertSoftwareEngineering {
				t.Fatalf("expert.type = %q, want %q (agent=%q)", got.Expert.Type, ExpertSoftwareEngineering, tt.agentName)
			}
		})
	}
}

// ─── Boundary: long system prompt still identifies expert ───

func TestDetectExpertFromSystemPrompt_LongSystemPrompt(t *testing.T) {
	// Real-world system prompts are large. Expert identity usually appears in
	// the first 1-2 KB; we keep the truncation behavior consistent with
	// telemetry's MaxSystemRunes=8192 in normalizeMessages.
	prefix := strings.Repeat("You are an expert in distributed systems. ", 500) // ~16KB of fluff
	system := prefix + "You are a security researcher. Continue your work."
	if got := DetectExpertFromSystemPrompt(system); got != ExpertSecurity {
		t.Fatalf("long prompt must still detect expert; got %q", got)
	}
}
