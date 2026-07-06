package admin

import (
	"strings"
	"testing"
)

// TestProviderCodeLookupUsesDisplayNameColumn 回归测试：
//
// 旧版 LiveStreamSSEHub.ProviderCodeFor / ProviderCodeForCredential 里的 SQL
// 引用了 providers.name 列，但 sql/schema/01-schema.sql 里 providers 表只
// 有 display_name 没有 name —— pgx 会返回 ERROR: column "name" does not
// exist (42703) 并被忽略成空字符串，前端泳道就把供应商显示成"未知"。
//
// 老板反馈 Minimax 的 minimax-m3 在供应商泳道里显示空/未知，根因就在这里。
//
// 本测试只做静态校验：保证 ProviderCodeFor / ProviderCodeForCredential 的 SQL
// 不再引用 providers.name 列。如果有任何人无意回退这个修复，本测试会失败。
func TestProviderCodeLookupUsesDisplayNameColumn(t *testing.T) {
	cases := []struct {
		name string
		want string
		body string
	}{
		// providers table has no `name` column — fall back to providers.display_name.
		{"ProviderCodeFor", "NULLIF(display_name", providerCodeForSQLBody},
		// credentials join: must use p.display_name (column rename, not display_name bare).
		{"ProviderCodeForCredential", "p.display_name", providerCodeForCredentialSQLBody},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.Contains(c.body, "NULLIF(name") {
				t.Fatalf("%s SQL still references NULLIF(name, …); expected %s. body=\n%s", c.name, c.want, c.body)
			}
			if !strings.Contains(c.body, c.want) {
				t.Fatalf("%s SQL does not reference %s; column rename may be incomplete. body=\n%s", c.name, c.want, c.body)
			}
			if !strings.Contains(c.body, "FROM providers") && !strings.Contains(c.body, "JOIN providers") {
				t.Fatalf("%s SQL lost its reference to providers? body=\n%s", c.name, c.body)
			}
		})
	}
}
