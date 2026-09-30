package admin

// vendor_recent_failures_sql_test.go — R75 P1 离线契约门。
//
// 真库门 deploy/sql/verify/vendor_recent_failures_pg_test.go 证明
// VendorRecentFailuresSQL 的**语义**（不扇出、不错配、LIMIT 限失败条数），
// 但它以 LLM_GATEWAY_SUPPLIER_PG_DSN 为门控，无 PG 环境即跳过。本门补上离线
// 那一段：只断言 SQL 必须同时具备的四个要素。
//
// 边界说明（别把它当语义门）：本门只能证明「四要素都在」，证明不了「因此语义
// 正确」——那正是真库门的职责。任何把本门当充分条件的读法都是错的。
//
// 判别力（变异验证已做，见 45 号报告）：
//   - 去掉 LATERAL 限 1（改回普通 LEFT JOIN）→ 真库门红；
//   - 去掉 error_kind 定位键 → 真库门红（预览跨尝试错配）；
//   - 把外层子查询的 LIMIT 10 去掉 → 真库门红（返回全部行）。

import (
	"strings"
	"testing"
)

func TestVendorRecentFailuresSQLContract(t *testing.T) {
	s := VendorRecentFailuresSQL

	// 1. 回补必须是 LATERAL ... LIMIT 1：普通 LEFT JOIN 会让一条失败扇出成多行。
	//    顺序也重要——外层先取 10 条不同失败，再回补；反过来 LIMIT 作用在
	//    扇出后的连接行上。
	if !strings.Contains(s, "LEFT JOIN LATERAL") {
		t.Error("preview backfill must be a LATERAL join; a plain LEFT JOIN fans one failure out into N rows")
	}

	// 2. LATERAL 内部必须限 1 行。LATERAL 本身不保证单行。
	lateral := s[strings.Index(s, "LEFT JOIN LATERAL"):]
	if !strings.Contains(lateral, "LIMIT 1") {
		t.Error("LATERAL backfill must carry LIMIT 1")
	}

	// 3. 定位键必须含 error_kind。(request_id, credential_id, attempt_index)
	//    不是 candidate_failure_logs 的唯一键——准入降级与随后的上游失败共用
	//    这三列；漏掉 error_kind 会把上游 body 贴到准入事件那一行。
	for _, frag := range []string{
		"cf.request_id = u.request_id",
		"cf.credential_id = u.credential_id",
		"cf.attempt_index = u.attempt_seq",
		"cf.error_kind = u.error_type",
	} {
		if !strings.Contains(lateral, frag) {
			t.Errorf("preview backfill locator missing %q — without the full key the panel "+
				"attaches one error's upstream body to another error's row", frag)
		}
	}

	// 4. 只有真正带上游 body 的行才提供预览。准入拒绝对应的 c 行 preview 为
	//    NULL，不加此条件时它会从「同 attempt 的兄弟行」那里借到一个 body。
	if !strings.Contains(lateral, "cf.upstream_response_preview IS NOT NULL") {
		t.Error("preview backfill must require a non-null preview; admission-rejection rows carry none")
	}

	// 5. 外层：先子查询取 10 条不同失败，且必须有 ORDER BY（否则 LIMIT 取哪 10 条不确定）。
	outer := s[:strings.Index(s, "LEFT JOIN LATERAL")]
	if !strings.Contains(outer, "ORDER BY occurred_at DESC") {
		t.Error("outer subquery must order before LIMIT; unordered LIMIT 10 is nondeterministic")
	}
	if !strings.Contains(outer, "LIMIT 10") {
		t.Error("outer subquery must cap at 10 distinct failures before backfill")
	}

	// 6. 跨 citus-columnar 分区的 unified 视图不能用 SELECT *：直接触发
	//    "cache lookup failed for attribute source of relation" (XX000)。
	//    本轮第一版修法就是这样被真库当场否掉的。
	if strings.Contains(outer, "SELECT *") {
		t.Error("outer subquery must list columns explicitly; SELECT * over the unified view " +
			"aborts on citus-columnar with XX000 cache lookup failed")
	}
}
