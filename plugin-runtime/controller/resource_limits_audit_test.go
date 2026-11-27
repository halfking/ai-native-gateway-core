package controller

import (
	"os"
	"path/filepath"
	"testing"

	root "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/limiter"
)

// writeConfigBody 写带 default_resource_limits 的 gateway.yaml（writeConfig
// 的资源限制扩展版）。
func writeConfigWithLimits(t *testing.T, path, dir, enabled string, withLimits bool) {
	t.Helper()
	limits := ""
	if withLimits {
		limits = `
  resource_limits_enabled: true
  default_resource_limits:
    memory:
      limit: "512M"
    cpu:
      limit: "50%"
    max_processes: 10
`
	}
	data := "server:\n  host: localhost\n  port: 8080\nplugins:\n  directory: " + `"` + dir + `"` + "\n  enabled: [" + enabled + "]\n" + limits
	if err := os.WriteFile(path+".new", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

// P0 回归（2026-11-27 审计）：scan() 合并 gateway.yaml 默认限制后，内存态
// Manifest 带 resource_limits 而磁盘 bundle 没有；snapshot() 的
// sameManifest 比对曾据此误判「manifest 在快照期间被改变」⇒ 插件拒启。
// 本测试锁 start→snapshot→Start 全链路：配置默认限制后插件必须照常 ready。
func TestControllerStartsWithDefaultResourceLimits(t *testing.T) {
	plugins, path := fixture(t)
	// fixture 已写过无限制的 config；重写为带默认限制的同名配置
	writeConfigWithLimits(t, path, plugins, "one, two", true)
	c := startController(t, path)
	if c.BaseURL("one") == "" || c.BaseURL("two") == "" {
		t.Fatal("plugins did not become ready with default_resource_limits configured")
	}
	// manifest 中回填的默认限制对观测面可见
	for _, id := range []string{"one", "two"} {
		m := c.Manifest(id)
		if m == nil || m.ResourceLimits == nil {
			t.Fatalf("plugin %s manifest lost merged resource_limits", id)
		}
		if m.ResourceLimits.Memory == nil || m.ResourceLimits.Memory.Limit != "512M" {
			t.Errorf("plugin %s memory limit = %+v, want 512M", id, m.ResourceLimits.Memory)
		}
		if rl := c.Supervisor().LimiterOf(id); rl == nil {
			t.Errorf("plugin %s has no limiter after start", id)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// snapshot 比对豁免只针对「内存版默认值」：磁盘 manifest 自带
// resource_limits 时仍是比对的一部分（bundle 完整性不放松）。
func TestSnapshotKeepsManifestDeclaredLimitsInComparison(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.json")
	body := `{"plugin_id":"x","plugin_version":"1.0.0","gateway_compatibility":{"api_contract":"gateway-plugin-v1"},"runtime":{"entrypoint":"run","handshake_path":"/plugin/handshake","health_path":"/plugin/healthz"},"resource_limits":{"max_processes":3}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := root.LoadManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.ResourceLimits == nil || m.ResourceLimits.MaxProcesses != 3 {
		t.Fatalf("fixture manifest limits not parsed: %+v", m.ResourceLimits)
	}
	c := &Controller{runtimeDir: dir}
	// 同一 bundle 再 snapshot：manifest 声明的限制在双方都在 ⇒ 比对通过
	frozen, err := c.snapshot(m)
	if err != nil {
		t.Fatalf("snapshot with manifest-declared limits should pass: %v", err)
	}
	if frozen.ResourceLimits == nil || frozen.ResourceLimits.MaxProcesses != 3 {
		t.Errorf("frozen manifest limits lost: %+v", frozen.ResourceLimits)
	}
}

// limiter.AssignProcess 对 pid<=0 拒绝（P1 审计修复：fake pid 0 曾空转轮询）。
func TestAssignProcessRejectsInvalidPid(t *testing.T) {
	rl, err := limiter.NewResourceLimiter("audit-pid", limiter.ResourceLimits{MaxProcesses: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := rl.AssignProcess(0); err == nil {
		t.Fatal("pid 0 must be rejected")
	}
	if err := rl.AssignProcess(-1); err == nil {
		t.Fatal("negative pid must be rejected")
	}
	// monitor 模式未启动监控，Stop 应干净收尾
	if err := rl.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
