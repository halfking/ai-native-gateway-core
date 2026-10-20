package telemetry

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpdateRequestLogSQL_CoversEveryMutableLedgerColumn（★ 改指，原名
// TestUpdateRequestLogSQL_IncludesRoutingAndAttachments）guards the same class
// of gap as t0..t9: the terminal Update path must refresh **every** column the
// Insert path writes, otherwise the row keeps the provisional values.
//
// 旧门（routing_attempts / routing_summary / attachments / canonical_model /
// t0_arrived_at）守的是「v1 宽表的 UPDATE 必须赋值 INSERT 也赋的那些列」。
// v1 族随 890 整族删除 ⇒ 那批列不存在了 ⇒ 断言对象已不存在。
//
// 改指后守的是**同一件事在新写入路径上的形态**（usage_ledger_hot）：
//
//	① INSERT 写过的**每一个可变列**，10 参（带真实 token）的 UPDATE 变体
//	   都必须能刷新——终态回填是「把 provisional 行补成终态行」，漏一列就
//	   留下一列 provisional 的值。
//	② UPDATE 的 SET 列**不得**超出 INSERT 的列清单——超出的列在运行期是
//	   42703（列不存在），而这条门能在 CI 里抓住它。
//	③ 5 参（无 token）变体只刷新它能知道的子集，且必须是 10 参变体的子集。
//
// 这三条同时把旧门想守的「routing_attempts 之类不得被悄悄漏掉」继承了下来：
// 任何 v1-only 列（routing_attempts / attachments / canonical_model /
// t0_arrived_at …）一旦被重新写进 UPDATE，② 立刻变红。
func TestUpdateRequestLogSQL_CoversEveryMutableLedgerColumn(t *testing.T) {
	src, err := os.ReadFile("client.go")
	require.NoError(t, err)
	text := string(src)

	insertCols := insertLedgerColumns(t, text)
	require.NotEmpty(t, insertCols, "正向对照：必须解析出 INSERT 的列清单")

	fn := extractFunc(text, "func (c *Client) updateRequestLog")
	require.NotEmpty(t, fn, "updateRequestLog not found")
	variants := extractUpdateVariants(fn, "UPDATE usage_ledger_hot")
	require.Len(t, variants, 2, "新写入路径应当有两个 UPDATE 变体（10 参 / 5 参）")

	// ① ③：逐个变体核对 SET 列。
	var tenArgCols, slimArgCols []string
	for _, variant := range variants {
		cols := variantSetColumns(t, variant)
		require.NotEmpty(t, cols, "正向对照：变体里必须解析出 SET 列")
		for _, col := range cols {
			require.Contains(t, insertCols, col,
				"UPDATE 设置了 INSERT 不写的列 %q —— 运行期必然 42703（列不存在）", col)
		}
		if len(cols) >= len(tenArgCols) {
			tenArgCols = cols
		} else {
			slimArgCols = cols
		}
	}
	require.NotEmpty(t, tenArgCols)
	require.NotEmpty(t, slimArgCols, "正向对照：必须解析出 5 参（无 token）变体")
	for _, col := range slimArgCols {
		require.Contains(t, tenArgCols, col,
			"5 参变体的列 %q 必须也在 10 参变体里（两个变体刷新的是同一批列）", col)
	}

	// ①：INSERT 写过的每一个可变列，10 参变体都必须能刷新。
	// 不可变列 = 行的身份/维度列 + 时间戳 + 倍率（它们在 INSERT 时定下，
	// 终态回填不重写）。
	immutable := map[string]bool{
		"request_id": true, "ts": true, "tenant_id": true, "application_id": true,
		"api_key_id": true, "end_user_id": true, "credential_id": true,
		"provider_id": true, "canonical_id": true, "raw_model_name": true,
		"rate_multiplier": true,
	}
	missing := []string{}
	for _, col := range insertCols {
		if immutable[col] {
			continue
		}
		if !contains(tenArgCols, col) {
			missing = append(missing, col)
		}
	}
	require.Empty(t, missing,
		"终态 UPDATE 必须能刷新 INSERT 写过的每一个可变列；漏了：%v", missing)
}

// insertLedgerColumns 解析 insertRequestLog 的 INSERT 列清单。
func insertLedgerColumns(t *testing.T, text string) []string {
	t.Helper()
	fn := extractFunc(text, "func (c *Client) insertRequestLog")
	require.NotEmpty(t, fn, "insertRequestLog not found")
	sqlStart := strings.Index(fn, "INSERT INTO usage_ledger_hot (")
	require.GreaterOrEqual(t, sqlStart, 0, "INSERT INTO usage_ledger_hot not found")
	sqlEnd := strings.Index(fn[sqlStart:], "`") + sqlStart
	sql := fn[sqlStart:sqlEnd]
	return parseIdentList(sql, sqlIndex(sql, "(", 0)+1, sqlIndex(sql, ") VALUES", 0))
}

// variantSetColumns 解析一个 UPDATE 变体的 SET 列清单。
func variantSetColumns(t *testing.T, variant updateVariant) []string {
	t.Helper()
	sql := variant.sql
	setStart := strings.Index(sql, "SET")
	whereStart := strings.Index(sql, "WHERE request_id")
	require.GreaterOrEqual(t, setStart, 0)
	require.Greater(t, whereStart, setStart)
	setSec := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(sql[setStart+3:whereStart], "")
	assignSplit := regexp.MustCompile(`\n\s*,?\s*([a-z_0-9]+)\s*=\s*`)
	var cols []string
	for _, name := range assignSplit.FindAllStringSubmatch("\n"+setSec, -1) {
		cols = append(cols, name[1])
	}
	return cols
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
