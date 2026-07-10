package admin

import (
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestCloneModuleStatusMap_IndependentCopy(t *testing.T) {
	src := map[string]ModuleWithStatus{
		"a": {Enabled: true},
	}
	clone := cloneModuleStatusMap(src)
	clone["a"] = ModuleWithStatus{Enabled: false}
	if !src["a"].Enabled {
		t.Fatalf("mutating clone must not affect source")
	}
	if clone["a"].Enabled {
		t.Fatalf("clone was not actually mutated")
	}
}

func TestCascadeDeps_OnlyRequiredDisabled(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, false)
	registerCascadeModule(t, "b.enabled", settings.Warning, false)
	registerCascadeModule(t, "c.enabled", settings.Warning, false)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled", Dependencies: []ModuleDependency{
			{Key: "a", Required: true},
			{Key: "b", Required: true},
			{Key: "c", Required: false},
		}},
		{Key: "a", SettingKey: "a.enabled"},
		{Key: "b", SettingKey: "b.enabled"},
		{Key: "c", SettingKey: "c.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	cascaded, err := cascadeEnableDepsWithWriter(defs, statuses, "root", w.write, w.rollback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cascaded) != 2 {
		t.Fatalf("expected 2 cascaded deps (a,b), got %v", cascaded)
	}
	if !containsCascade(cascaded, "a") || !containsCascade(cascaded, "b") {
		t.Fatalf("cascaded missing required deps: %v", cascaded)
	}
	if containsCascade(cascaded, "c") {
		t.Fatalf("optional dep c should not be cascaded")
	}
	if w.written["a.enabled"] != true || w.written["b.enabled"] != true {
		t.Fatalf("writer was not invoked as expected: %+v", w.written)
	}
}

func TestCascadeDeps_SkipsAlreadyEnabled(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, true) // default on
	registerCascadeModule(t, "b.enabled", settings.Warning, false)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled", Dependencies: []ModuleDependency{
			{Key: "a", Required: true},
			{Key: "b", Required: true},
		}},
		{Key: "a", SettingKey: "a.enabled"},
		{Key: "b", SettingKey: "b.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	cascaded, err := cascadeEnableDepsWithWriter(defs, statuses, "root", w.write, w.rollback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cascaded) != 1 || cascaded[0] != "b" {
		t.Fatalf("expected only b, got %v", cascaded)
	}
}

func TestCascadeDeps_NoRequiredDeps(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, false)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled"},
		{Key: "a", SettingKey: "a.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	cascaded, err := cascadeEnableDepsWithWriter(defs, statuses, "root", w.write, w.rollback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cascaded) != 0 {
		t.Fatalf("expected empty cascaded, got %v", cascaded)
	}
	if len(w.written) != 0 {
		t.Fatalf("writer should not have been called, got %+v", w.written)
	}
}

func TestCascadeDeps_RollsBackOnWriterError(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, false)
	registerCascadeModule(t, "b.enabled", settings.Warning, false)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled", Dependencies: []ModuleDependency{
			{Key: "a", Required: true},
			{Key: "b", Required: true},
		}},
		{Key: "a", SettingKey: "a.enabled"},
		{Key: "b", SettingKey: "b.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	w.failOn[bSpec] = errors.New("disk full")

	cascaded, err := cascadeEnableDepsWithWriter(defs, statuses, "root", w.write, w.rollback)
	if err == nil {
		t.Fatal("expected writer error to surface")
	}
	// 已经成功开启的依赖必须在错误时回滚
	if !w.rolledBack[aKey] {
		t.Fatalf("expected a to be rolled back, got %+v", w.rolledBack)
	}
	if w.rolledBack[bKey] {
		t.Fatalf("b should not be in rolled-back set (write failed before success), got %+v", w.rolledBack)
	}
	if len(cascaded) != 0 {
		t.Fatalf("cascaded should be empty on error, got %v", cascaded)
	}
}

func TestCascadeDeps_DangerousDepsNotAutoEnabled(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, false)
	registerCascadeModule(t, "b.enabled", settings.Dangerous, false)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled", Dependencies: []ModuleDependency{
			{Key: "a", Required: true},
			{Key: "b", Required: true},
		}},
		{Key: "a", SettingKey: "a.enabled"},
		{Key: "b", SettingKey: "b.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	_, err := cascadeEnableDepsWithWriter(defs, statuses, "root", w.write, w.rollback)
	if err == nil {
		t.Fatal("expected dangerous dep to be rejected")
	}
	if !w.rolledBack[aKey] {
		t.Fatalf("a.enabled should have been rolled back when b failed, got %+v", w.rolledBack)
	}
}

// TestApplyCascadeEnable_RollsBackOnRootWriteFailure 覆盖最关键兜底：
// "依赖都已成功开启 → 主模块写盘失败 → 关闭已开启的依赖"，保证不留半开状态。
func TestApplyCascadeEnable_RollsBackOnRootWriteFailure(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, false)
	registerCascadeModule(t, "b.enabled", settings.Warning, false)
	registerCascadeModule(t, "root.enabled", settings.Warning, true)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled", Dependencies: []ModuleDependency{
			{Key: "a", Required: true},
			{Key: "b", Required: true},
		}},
		{Key: "a", SettingKey: "a.enabled"},
		{Key: "b", SettingKey: "b.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	w.failOn["root.enabled"] = errors.New("disk full")
	root := &ModuleDefinition{Key: "root", SettingKey: "root.enabled"}

	cascaded, err := applyCascadeEnable(defs, statuses, root, w.write, w.rollback)
	if err == nil {
		t.Fatal("expected root write failure to surface")
	}
	if cascaded != nil {
		t.Fatalf("cascaded should be nil on root failure, got %v", cascaded)
	}
	if w.written[aSpec] != true {
		t.Fatalf("a.enabled should have been enabled before root failure, got %+v", w.written)
	}
	if w.written[bSpec] != true {
		t.Fatalf("b.enabled should have been enabled before root failure, got %+v", w.written)
	}
	if !w.rolledBack[aKey] || !w.rolledBack[bKey] {
		t.Fatalf("both deps should be rolled back, got %+v", w.rolledBack)
	}
}

// TestApplyCascadeEnable_SuccessReturnsCascaded 验证全成功路径：返回 cascaded 列表，且所有写盘发生。
func TestApplyCascadeEnable_SuccessReturnsCascaded(t *testing.T) {
	settings.Global = settings.NewRegistry()
	registerCascadeModule(t, "a.enabled", settings.Warning, false)
	registerCascadeModule(t, "root.enabled", settings.Warning, true)

	defs := []ModuleDefinition{
		{Key: "root", SettingKey: "root.enabled", Dependencies: []ModuleDependency{
			{Key: "a", Required: true},
		}},
		{Key: "a", SettingKey: "a.enabled"},
	}
	statuses := moduleStatusMap(defs)
	w := newCascadeMemWriter()
	root := &ModuleDefinition{Key: "root", SettingKey: "root.enabled"}

	cascaded, err := applyCascadeEnable(defs, statuses, root, w.write, w.rollback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cascaded) != 1 || cascaded[0] != aKey {
		t.Fatalf("expected cascaded=[a], got %v", cascaded)
	}
	if w.written[aSpec] != true {
		t.Fatalf("a.enabled should be enabled, got %+v", w.written)
	}
	if w.written["root.enabled"] != true {
		t.Fatalf("root.enabled should be enabled, got %+v", w.written)
	}
	if len(w.rolledBack) != 0 {
		t.Fatalf("rollback should not be called on success, got %+v", w.rolledBack)
	}
}

const (
	aKey  = "a"
	bKey  = "b"
	aSpec = "a.enabled"
	bSpec = "b.enabled"
)

type cascadeMemWriter struct {
	written    map[string]bool
	rolledBack map[string]bool
	failOn     map[string]error
}

func newCascadeMemWriter() *cascadeMemWriter {
	return &cascadeMemWriter{
		written:    make(map[string]bool),
		rolledBack: make(map[string]bool),
		failOn:     make(map[string]error),
	}
}

func (c *cascadeMemWriter) write(_ settings.Scope, key string, value bool) error {
	if err, ok := c.failOn[key]; ok {
		return err
	}
	c.written[key] = value
	return nil
}

func (c *cascadeMemWriter) rollback(keys []string) {
	for _, k := range keys {
		c.rolledBack[k] = true
	}
}

func registerCascadeModule(t *testing.T, key string, danger settings.DangerLevel, defaultValue bool) {
	t.Helper()
	if err := settings.Global.RegisterSpec(&settings.Spec{
		Key: key, Type: settings.TypeBool, Scope: settings.ScopePlatform,
		Category: settings.CategoryModules, Default: defaultValue, DangerLevel: danger,
	}); err != nil {
		t.Fatalf("register spec %s: %v", key, err)
	}
}

func containsCascade(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
