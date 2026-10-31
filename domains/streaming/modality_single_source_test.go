package streaming

import (
	"os"
	"regexp"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/tests/testutil"
)

// 「只有一份」轮的第三道门：请求模态词表的**分母锚在事实源上**。
//
// ★ 为什么需要这道门（LESSONS §559 通用式二：门的分母必须锚在事实源上，
//
//	否则「缩小分母 + 改判据」可以合谋出假绿）：
//	请求模态词表（provider.ModalityRequestModalities）是三份判据的**请求轴**，
//	也是错误载荷 supported_modalities 的**发布清单来源**。
//	⇒ 词表被悄悄缩小 ⇒ 对拍域与发布清单同步缩小 ⇒ 门照样绿 ⇒
//
// 而「本该被发布/被对拍的模态」整族从视野里消失。
//
//	⇒ 词表被悄悄扩大 ⇒ 判据在域外漂了也可能检不出。
//
// ★ 双向锚定（两条事实源）：
//
//	① 登记模态词表 = models_canonical.modality 的 CHECK 约束
//	   （由 testutil.CanonicalModalityValueDomain 从迁移 451 派生）——
//	   请求侧与登记侧共用同一套模态词汇 ⇒ 请求词表 ⊇ 登记词表。
//	② 「image」是**只出现在请求侧**的模态（/v1/images/* 专用端点声明它，
//	   不在登记词表里）⇒ 请求词表 = 登记词表 ∪ {image}。
//
// ⇒ 两条合起来：词表必须**恰好**等于「登记词表 ∪ {image}」。
//
// ★★ 顺带覆盖「detect 产出的模态必须被词表收下」：detect 的返回常量是
//
//	chat 路径请求模态的事实源（TestAlternativesSQL_DetectModalityDomainClosed
//	守的是「SQL 有臂」，这条守的是「词表收得下」）。
func TestModalityRequestUniverse_AnchoredToFactSource(t *testing.T) {
	universe := provider.ModalityRequestModalities
	inUniverse := func(v string) bool {
		for _, u := range universe {
			if u == v {
				return true
			}
		}
		return false
	}

	// ① 登记词表（事实源）⇒ 请求词表必须收下每一个。
	registered, err := testutil.CanonicalModalityValueDomain("..")
	if err != nil {
		t.Fatalf("派生登记模态值域失败：%v", err)
	}
	for _, reg := range registered {
		if reg == "" {
			continue
		}
		if !inUniverse(reg) {
			t.Errorf("请求模态词表里缺少登记词表的事实源值 %q（事实源=models_canonical.modality 的 CHECK 约束）—— ★ 该模态既是登记值也是请求值（/v1/embeddings 就传它）⇒ 必须进词表，否则对拍域与发布清单会整族漏掉它", reg)
		}
	}

	// ② 请求词表 ⊆ 登记词表 ∪ {image}。
	for _, req := range universe {
		if req == imageModalityRequest {
			continue
		}
		found := false
		for _, reg := range registered {
			if reg == req {
				found = true
			}
		}
		if !found {
			t.Errorf("请求模态词表里的 %q 既不在登记词表（事实源=models_canonical.modality 的 CHECK 约束）里，也不是 imageModalityRequest —— ★ 词表里多出了事实源没有的值 ⇒ 要么它是事实源漏了，要么词表写错了", req)
		}
	}

	// ★③ detect 的返回常量必须落在词表内（chat 路径请求模态的事实源）。
	src, err := os.ReadFile("modality_detect.go")
	if err != nil {
		t.Fatalf("读 modality_detect.go 失败：%v", err)
	}
	constDefRE := regexp.MustCompile(`(?m)^\s*(modality[A-Za-z0-9_]*)\s*=\s*"([^"]*)"`)
	returnRE := regexp.MustCompile(`return\s+(modality[A-Za-z0-9_]*)`)
	constValues := map[string]string{}
	for _, m := range constDefRE.FindAllStringSubmatch(string(src), -1) {
		constValues[m[1]] = m[2]
	}
	returned := map[string]bool{}
	for _, m := range returnRE.FindAllStringSubmatch(string(src), -1) {
		returned[m[1]] = true
	}
	if len(returned) == 0 {
		t.Fatalf("modality_detect.go 里没解析出任何 `return modalityX` —— 源文件形态变了，本判据失效")
	}
	for name := range returned {
		val, ok := constValues[name]
		if !ok {
			t.Errorf("detect 返回了 %s，但它没有对应的字面值常量定义（源文件形态变了？）", name)
			continue
		}
		if !inUniverse(val) {
			t.Errorf("detectRequestModality 可能产出请求模态 %q（%s）⇒ 它不在请求模态词表里 ⇒ ★ 词表必须收下它（否则对拍域与发布清单会漏掉这一整族）", val, name)
		}
	}
}
