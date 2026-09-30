// Command gen-env-ref 把 settings 包的全部配置规格（EnvName/默认值/描述）
// 导出为 Markdown 表，作为 envs.samples/SPECS-REFERENCE.md 的生成器。
// 该文件是机器生成物，勿手改；再生成:
//
//	go run ./scripts/gen-env-ref > envs.samples/SPECS-REFERENCE.md
//
// 数据源为 settings.PlatformSpecs()/TenantSpecs() 注册表与模块级
// Goal/Handoff/AutoControl 规格，与线上热加载通道同源，保证文档零漂移。
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func main() {
	type row struct {
		env, key, scope, typ, def, opts, desc string
	}
	seen := map[string]bool{}
	var rows []row

	add := func(env, key, scope string, s *settings.Spec) {
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
				lo = fmt.Sprintf("%g", *s.Min)
			}
			if s.Max != nil {
				hi = fmt.Sprintf("%g", *s.Max)
			}
			opts = lo + " ~ " + hi
		}
		desc := s.Description
		if s.Unit != "" && !strings.Contains(desc, s.Unit) {
			desc += "（" + s.Unit + "）"
		}
		rows = append(rows, row{
			env: s.EnvName, key: s.Key, scope: scope,
			typ: fmt.Sprintf("%v", s.Type), def: fmt.Sprintf("%v", s.Default),
			opts: opts, desc: desc,
		})
	}

	for _, s := range settings.PlatformSpecs() {
		add(s.EnvName, s.Key, "platform", s)
	}
	for _, s := range settings.TenantSpecs() {
		add(s.EnvName, s.Key, "tenant", s)
	}
	for i := range settings.GoalSpecs() {
		add("", "", "tenant", &settings.GoalSpecs()[i])
	}
	for i := range settings.HandoffSpecs() {
		add("", "", "tenant", &settings.HandoffSpecs()[i])
	}
	for i := range settings.AutoControlSpecs() {
		add("", "", "tenant", &settings.AutoControlSpecs()[i])
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
