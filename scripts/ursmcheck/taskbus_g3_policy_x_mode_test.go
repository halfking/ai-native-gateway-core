package ursm_test

// taskbus_g3_policy_x_mode_test.go — G3 策略×模式：terminate/suspend 不得挂 TaskBus。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/phasepolicy"
)

func taskbusG3Root(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root missing go.mod: %s", root)
	}
	return root
}

func TestTaskBusG3_NoTaskBusOnTerminateOrSuspend(t *testing.T) {
	root := taskbusG3Root(t)
	path := filepath.Join(root, "cmd", "gateway", "main_pipeline.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	// 对拍实际 AddStage：抓 Phase: pipeline.PhaseX 与同段 Hooks。
	stageRe := regexp.MustCompile(`(?s)AddStage\(&pipeline\.PipelineStage\{(.*?)\}\)`)
	phaseRe := regexp.MustCompile(`Phase:\s*pipeline\.(Phase\w+)`)
	taskBusHookRe := regexp.MustCompile(`NewCacheSaveTaskHook|DispatchTaskBus|taskbus`)

	forbidden := map[string]bool{}
	for _, p := range phasepolicy.Policies() {
		if p.Policy == phasepolicy.PolicyTerminate || p.Policy == phasepolicy.PolicySuspend {
			// PhaseAuthentication → authentication constant value check via name
			forbidden[p.Phase] = true
		}
	}

	phaseConstToValue := map[string]string{
		"PhasePreAuthentication": "pre_authentication", "PhaseAuthentication": "authentication",
		"PhasePostAuthentication": "post_authentication", "PhasePreRouting": "pre_routing",
		"PhaseRouting": "routing", "PhasePostRouting": "post_routing",
		"PhaseGovernance": "governance", "PhasePreTransform": "pre_transform",
		"PhaseTransform": "transform", "PhasePostTransform": "post_transform",
		"PhasePreUpstream": "pre_upstream", "PhaseUpstream": "upstream",
		"PhasePostUpstream": "post_upstream", "PhasePreResponse": "pre_response",
		"PhaseResponse": "response", "PhasePostResponse": "post_response",
	}

	for _, m := range stageRe.FindAllStringSubmatch(src, -1) {
		block := m[1]
		pm := phaseRe.FindStringSubmatch(block)
		if pm == nil {
			continue
		}
		val := phaseConstToValue[pm[1]]
		if !forbidden[val] {
			continue
		}
		if taskBusHookRe.MatchString(block) {
			t.Fatalf("G3: TaskBus signal in terminate/suspend phase %s (%s)", pm[1], val)
		}
	}

	// 正向：post_response 的 cache_save 示范迁必须在场（对拍实际挂载）。
	if !strings.Contains(src, "NewCacheSaveTaskHook") {
		t.Fatal("G3 setup: expected cache_save TaskBus demo mount NewCacheSaveTaskHook in main_pipeline.go")
	}
	// A4: memora 不得假装已挂载
	if strings.Contains(src, "NewMemoraAutoHook") {
		t.Fatal("A4: main_pipeline must not mount NewMemoraAutoHook this wave")
	}
}
