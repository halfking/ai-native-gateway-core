package sanitize

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// ★ 规则说明必须从 YAML 一路走到规则模型（2026-10-29 输出合规误报轮）。
//
// ★ 缺陷本体：★ YAML 里**一直写着** `description:`（★ 如 pii.email 的
// 「电子邮箱地址」）★★ 而装载器**没有读它** ⇒ ★★ 说明在装载时就被丢掉 ⇒ ★★
// 管理面「规则说明」永远是空的 ⇒ ★★ 运维「看不懂这条规则在判什么」⇒ ★★
// 误报只能靠猜 ★★。
//
// ★ 判据：★ 说明要从 YAML 走到 `RuleConfig()` 的每条规则上 ★★
// ⇒ 这条测试就是「说明不许再被悄悄丢掉」的守门人。
func TestYAMLDescriptionsReachRuleConfig(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("加载 YAML 失败: %v", err)
	}
	cfg := detector.RuleConfig()
	if len(cfg.Rules) == 0 {
		t.Fatal("规则集为空")
	}
	described := 0
	byID := map[string]string{}
	for _, r := range cfg.Rules {
		byID[r.ID] = r.Description
		if strings.TrimSpace(r.Description) != "" {
			described++
		}
	}
	// ★ 至少要有一批规则带说明（★ YAML 里的 pii/secret/financial 都有）。
	if described == 0 {
		t.Fatal("没有任何规则带说明 ⇒ YAML 的 description 仍在装载时被丢掉")
	}
	// ★ 抽样核对一条：★ pii.email 的说明应当来自 YAML（「电子邮箱地址」）。
	desc, ok := byID["pii.email"]
	if !ok {
		t.Skipf("规则集里没有 pii.email（配置口径变了）：%v", byID)
	}
	if strings.TrimSpace(desc) == "" {
		t.Fatal("pii.email 没有说明 ⇒ YAML 的 description 没有走到规则模型")
	}
	if !strings.Contains(desc, "邮箱") {
		t.Fatalf("pii.email 的说明不像 YAML 里那一条: %q", desc)
	}
}

// ★ 说明必须登记进规则说明目录（★ 管理面「读规则」时靠它补齐历史快照）。
func TestYAMLDescriptionsRegisteredInCatalog(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("加载 YAML 失败: %v", err)
	}
	_ = detector.RuleConfig() //装载即登记
	got, ok := sanitizerules.RuleDescription("pii.email")
	if !ok {
		t.Fatal("规则说明目录里没有 pii.email ⇒ 管理面补不出历史快照的说明")
	}
	if !strings.Contains(got, "邮箱") {
		t.Fatalf("目录里的 pii.email 说明不对: %q", got)
	}
}
