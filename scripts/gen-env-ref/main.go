// Command gen-env-ref 把 settings 包的全部配置规格（EnvName/默认值/描述）
// 导出为 Markdown 表，作为 envs.samples/SPECS-REFERENCE.md 的生成器。
// 该文件是机器生成物，勿手改；再生成:
//
//	go run ./scripts/gen-env-ref > envs.samples/SPECS-REFERENCE.md
//
// 数据源为 settings.PlatformSpecs()/TenantSpecs() 注册表与模块级
// Goal/Handoff/AutoControl 规格（后三者未并入 TenantSpecs，需单列），
// 与线上热加载通道同源，保证文档零漂移。
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// fmtNum 用十进制定点格式输出数值，避免 %v/%g 对大整数产生
// 科学计数法（如 31536000 → 3.1536e+07）损害表格可读性。
func fmtNum(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func fmtDefault(def any) string {
	if f, ok := def.(float64); ok {
		return fmtNum(f)
	}
	return fmt.Sprintf("%v", def)
}

func main() {
	type row struct {
		env, key, scope, typ, def, opts, desc string
	}
	seen := map[string]bool{}
	var rows []row

	add := func(scope string, s *settings.Spec) {
		if s == nil || s.EnvName == "" || seen[s.EnvName] {
			return
		}
		seen[s.EnvName] = true
		opts := ""
		if len(s.Options) > 0 {
			opts = strings.Join(s.Options, " / ")
		} else if s.Min != nil || s.Max != nil {
			lo, hi := "", ""
			if s.Min != nil {
				lo = fmtNum(*s.Min)
			}
			if s.Max != nil {
				hi = fmtNum(*s.Max)
			}
			opts = lo + " ~ " + hi
		}
		desc := s.Description
		if s.Unit != "" && !strings.Contains(desc, s.Unit) {
			desc += "（" + s.Unit + "）"
		}
		rows = append(rows, row{
			env: s.EnvName, key: s.Key, scope: scope,
			typ: fmt.Sprintf("%v", s.Type), def: fmtDefault(s.Default),
			opts: opts, desc: desc,
		})
	}

	for _, s := range settings.PlatformSpecs() {
		add("platform", s)
	}
	for _, s := range settings.TenantSpecs() {
		add("tenant", s)
	}
	// Goal/Handoff/AutoControl 未注册进 TenantSpecs，这里单列补全
	// （seen 去重保证未来并入后不重复）。
	for _, specs := range [][]settings.Spec{
		settings.GoalSpecs(), settings.HandoffSpecs(), settings.AutoControlSpecs(),
	} {
		for i := range specs {
			add("tenant", &specs[i])
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].env < rows[j].env })

	out := os.Stdout
	fmt.Fprintln(out, "<!--")
	fmt.Fprintln(out, "本文件由 scripts/gen-env-ref 生成，勿手改。")
	fmt.Fprintln(out, "再生成: go run ./scripts/gen-env-ref > envs.samples/SPECS-REFERENCE.md")
	fmt.Fprintln(out, "-->")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "# settings 配置规格全表（机器生成）")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "settings 注册表（PlatformSpecs/TenantSpecs/模块级规格）中带 EnvName 的全部条目，")
	fmt.Fprintln(out, fmt.Sprintf("共 %d 个环境变量。这些变量均可被 settings_kv 数据库值按 Key 覆盖（热加载），", len(rows)))
	fmt.Fprintln(out, "优先级: settings_kv > 环境变量 > Default 列。作用域 platform=平台级、tenant=租户级。")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "| 环境变量 | 设置键 | 作用域 | 类型 | 默认值 | 选项/范围 | 说明 |")
	fmt.Fprintln(out, "| --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range rows {
		esc := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
		fmt.Fprintf(out, "| `%s` | `%s` | %s | %s | `%s` | %s | %s |\n",
			esc(r.env), esc(r.key), r.scope, r.typ, esc(r.def), esc(r.opts), esc(r.desc))
	}
}
