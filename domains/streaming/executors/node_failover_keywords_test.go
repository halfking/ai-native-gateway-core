package executors

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// Wave 3 B5① 镜像锚点：LoadRetryKeywords 的内置默认必须与 settings 侧
// spec 默认（DefaultContinueKeywordsJSON / DefaultRetryKeywordsJSON）逐词
// 一致。settings 不能 import domains，两侧只能靠本测试钉住同步。
func TestLoadRetryKeywords_DefaultsMirrorSettingsSpec(t *testing.T) {
	wantContinue := []string{"继续", "continue", "go", "come on", "请继续", "接着", "keep going", "继续回答", "接着说", "go on", "続けて", "続けてください", "このまま続けて", "続きを"}
	wantRetry := []string{"重试", "retry", "请重试", "再试一次", "try again", "重新回答", "再来", "再試行", "もう一度", "やり直してください", "retry please"}

	gotContinue, gotRetry := LoadRetryKeywords(nil)
	if !reflect.DeepEqual(gotContinue, wantContinue) {
		t.Fatalf("continue defaults drifted:\n got %v\nwant %v", gotContinue, wantContinue)
	}
	if !reflect.DeepEqual(gotRetry, wantRetry) {
		t.Fatalf("retry defaults drifted:\n got %v\nwant %v", gotRetry, wantRetry)
	}

	var specContinue, specRetry []string
	if err := json.Unmarshal([]byte(settings.DefaultContinueKeywordsJSON), &specContinue); err != nil {
		t.Fatalf("spec default unparsable: %v", err)
	}
	if err := json.Unmarshal([]byte(settings.DefaultRetryKeywordsJSON), &specRetry); err != nil {
		t.Fatalf("spec default unparsable: %v", err)
	}
	if !reflect.DeepEqual(specContinue, wantContinue) || !reflect.DeepEqual(specRetry, wantRetry) {
		t.Fatalf("settings spec defaults out of sync with executors defaults:\nspec      %v / %v\nexecutors %v / %v",
			specContinue, specRetry, wantContinue, wantRetry)
	}
}
