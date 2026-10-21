package ursm_test

// guard_declaration_fixture_test.go —— R92 轮：**闸门名单不变量门**的夹具。
//
// ★ 夹具纪律（与仓内既有纪律同款）：
//   ① 每个夹具一个**独立子目录**（= 独立包）⇒ 互不污染；
//   ② **载体形态与引用形态必须与被测判据同款**
//      —— 判据读的是 `var GuardFunctions = []string{...}` 这个**包级 var**
//      —— 夹具就写这个形态；跨包判据读的是 **import + 限定名调用**
//      —— 夹具就写这个形态（import 路径必须以 go.mod 的 module 开头）；
//   ③ 夹具放在 `t.TempDir()` ⇒ 不进仓、不参与 `go build ./...`；
//   ④ ★ **必须有一个合规基线夹具（fx00）** ⇒ 证明门不是「见什么报什么」，
//      也不是恒红门。
//
// ★ 夹具代码**不需要能编译**（本门是纯 AST 分析），但语法必须正确。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// r92Fixture 写一个夹具目录，返回它的根。
func r92Fixture(t *testing.T, base, name, module string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	all := map[string]string{"go.mod": "module " + module + "\n"}
	for k, v := range files {
		all[k] = v
	}
	for rel, content := range all {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return dir
}

// r92RunFixture 对一个夹具目录跑不变量，返回违规列表。
func r92RunFixture(t *testing.T, dir string) []string {
	t.Helper()
	modulePath := r87ModulePath(t, dir)
	if modulePath == "" {
		t.Fatalf("夹具 %s 没有可解析的 module 行", dir)
	}
	return r92GuardViolations(t, dir, modulePath, map[string]bool{})
}

// r92HasRule 违规列表里有没有某条规则（G1/G2/…）。
func r92HasRule(violations []string, rule string) bool {
	for _, v := range violations {
		if strings.Contains(v, rule) {
			return true
		}
	}
	return false
}

// fx00：合规基线 —— 声明正确、闸门合法、跨包调用 ⇒ **必须零违规**。
const fx00Gate = `package pkgA

import "testing"

var GuardFunctions = []string{
	"Gate",
}

func Gate(t *testing.T, dsn string) {
	if !strings.Contains(dsn, "SELECT current_database()") {
		t.Fatalf("gate")
	}
}
`

const fx00Use = `package pkgB

import (
	"testing"

	"example.com/x/fx00/pkgA"
)

func UseGate(t *testing.T, dsn string) {
	pkgA.Gate(t, dsn)
}
`

// TestGuardDeclarationInvariantsFixtures 夹具集。
func TestGuardDeclarationInvariantsFixtures(t *testing.T) {
	base := t.TempDir()

	t.Run("fx00_合规基线必须零违规", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx00", "example.com/x/fx00", map[string]string{
			"pkgA/gate.go": fx00Gate,
			"pkgB/use.go":  fx00Use,
		})
		violations := r92RunFixture(t, dir)
		for _, v := range violations {
			t.Errorf("★ 合规基线被误报：%s", v)
		}
		if len(violations) != 0 {
			t.Errorf("★ 合规基线应零违规，实得 %d 条", len(violations))
		}
	})

	t.Run("fx01_G1悬空", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx01", "example.com/x/fx01", map[string]string{
			"pkg/gate.go": `package pkg

var GuardFunctions = []string{
	"NotThere",
}
`,
		})
		violations := r92RunFixture(t, dir)
		if !r92HasRule(violations, "G1") {
			t.Errorf("★ 夹具 fx01 应触发 G1（名单里的名字本包里不存在），实得 %d 条违规：%v", len(violations), violations)
		}
	})

	t.Run("fx02_G2未导出", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx02", "example.com/x/fx02", map[string]string{
			"pkg/gate.go": `package pkg

import "testing"

var GuardFunctions = []string{
	"hiddenGate",
}

func hiddenGate(t *testing.T, dsn string) {
	if !strings.Contains(dsn, "SELECT current_database()") {
		t.Fatalf("gate")
	}
}
`,
		})
		violations := r92RunFixture(t, dir)
		if !r92HasRule(violations, "G2") {
			t.Errorf("★ 夹具 fx02 应触发 G2（名单里的名字未导出），实得 %d 条违规：%v", len(violations), violations)
		}
	})

	t.Run("fx03_G3不能中止", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx03", "example.com/x/fx03", map[string]string{
			"pkg/gate.go": `package pkg

import "strings"

var GuardFunctions = []string{
	"NoAbort",
}

func NoAbort(dsn string) bool {
	return strings.Contains(dsn, "SELECT current_database()")
}
`,
		})
		violations := r92RunFixture(t, dir)
		if !r92HasRule(violations, "G3") {
			t.Errorf("★ 夹具 fx03 应触发 G3（闸门不能中止这条腿），实得 %d 条违规：%v", len(violations), violations)
		}
	})

	t.Run("fx04_G4与目标无关", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx04", "example.com/x/fx04", map[string]string{
			"pkg/gate.go": `package pkg

import "testing"

var GuardFunctions = []string{
	"NoTarget",
}

func NoTarget(t *testing.T) {
	t.Fatalf("gate")
}
`,
		})
		violations := r92RunFixture(t, dir)
		if !r92HasRule(violations, "G4") {
			t.Errorf("★ 夹具 fx04 应触发 G4（闸门与目标库判定无关），实得 %d 条违规：%v", len(violations), violations)
		}
	})

	t.Run("fx05_G5只是核对落点", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx05", "example.com/x/fx05", map[string]string{
			"pkg/gate.go": `package pkg

import "testing"

type want struct {
	Database string
}

var GuardFunctions = []string{
	"PureCheck",
}

func PureCheck(t *testing.T, got string, w want) {
	_ = "SELECT current_database()"
	if got != w.Database {
		t.Fatalf("mismatch")
	}
}
`,
		})
		violations := r92RunFixture(t, dir)
		if !r92HasRule(violations, "G5") {
			t.Errorf("★ 夹具 fx05 应触发 G5（闸门只是核对落点），实得 %d 条违规：%v", len(violations), violations)
		}
	})

	t.Run("fx06_G6漏声明", func(t *testing.T) {
		dir := r92Fixture(t, base, "fx06", "example.com/x/fx06", map[string]string{
			// ★ pkgA 有一个**具备闸门语义**且**被别的包调用**的函数，
			//   但**没有**声明 GuardFunctions ⇒ 必须报 G6。
			"pkgA/gate.go": `package pkgA

import "testing"

func Gate(t *testing.T, dsn string) {
	if !strings.Contains(dsn, "SELECT current_database()") {
		t.Fatalf("gate")
	}
}
`,
			"pkgB/use.go": `package pkgB

import (
	"testing"

	"example.com/x/fx06/pkgA"
)

func UseGate(t *testing.T, dsn string) {
	pkgA.Gate(t, dsn)
}
`,
		})
		violations := r92RunFixture(t, dir)
		if !r92HasRule(violations, "G6") {
			t.Errorf("★ 夹具 fx06 应触发 G6（跨包闸门漏声明），实得 %d 条违规：%v", len(violations), violations)
		}
	})
}
