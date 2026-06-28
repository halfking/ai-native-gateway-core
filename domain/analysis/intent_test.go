package analysis

import "testing"

func TestIntentKind_Constants(t *testing.T) {
	// 不允许空字符串作为合法 IntentKind（避免未初始化值被误判）。
	if IntentKind("") == IntentChat {
		t.Fatal("empty string must not equal IntentChat")
	}
	// 各枚举值应唯一。
	seen := map[IntentKind]bool{}
	for _, k := range []IntentKind{
		IntentChat, IntentCode, IntentReasoning, IntentSummary,
		IntentTranslation, IntentExtraction, IntentToolUse, IntentUnclassified,
	} {
		if seen[k] {
			t.Errorf("duplicate IntentKind: %s", k)
		}
		seen[k] = true
	}
}

func TestEventType_Constants(t *testing.T) {
	seen := map[EventType]bool{}
	for _, e := range []EventType{
		EventRequestCompleted, EventSessionClosed, EventToolCompleted,
		EventApprovalDecided, EventFailureDetected,
	} {
		if seen[e] {
			t.Errorf("duplicate EventType: %s", e)
		}
		seen[e] = true
	}
}
