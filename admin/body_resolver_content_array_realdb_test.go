//go:build !integration

package admin

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// body_resolver_content_array_realdb_test.go —— 真库读数：`:response` 槽位
// 收下 content 分片数组之后，★ **原来被拒的那些回答现在端得出来了**。
//
// # 为什么要有这道门
//
// 单元测试证明的是「函数在一段手写的 JSON 上对」；★ **它证明不了「生产上
// 真的有这种形状、而且数量大」**。⇒ ⇒ 这道门读真库，★ 把两件事放在同一张
// 读数上：★ 「旧提取器给不出的」与「★ 新提取器给得出的」各自多少条。
//
// # 口径（★ 三个属性一起发布：★ 定义 / 时刻 / 单位）
//
//	· 定义 = 生产会话族正文（`session_bodies` ＋ `session_bodies_hot`）里
//	  **最近 N 行**（★ 按 `ts` 倒序，★ 不去重：★ 一行＝一次回答）；
//	· N = `TEST_CONTENT_ARRAY_SAMPLE`（★ 默认 20000 行）；
//	· 单位 = **行（一次回答）**；
//	· 判据 = **同一个** `response_delta` 上，两个提取器各自给不给得出正文：
//	    `controlledString`（旧的：只收纯字符串）vs `controlledText`（新的）。
//
// # 三条断言（★ 每条都有理由，★ 不是「跑出来是多少就写多少」）
//
//	① **无回归**：★ 旧提取器给得出的，★ 新提取器必须也给得出；
//	② **有救援**：★ 旧提取器给不出、而新提取器给得出的，★ 必须存在且不为零
//	   （★ 否则这次改动在真实数据上什么都没修）；
//	③ **恒等式**：★ 「给不出的」＝「救回的」＋「仍然给不出的」——★ 一行都不能
//	   靠总数平衡溜过去。

const contentArraySampleDefault = 20000

func TestControlledBodyContentArrayInRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set — offline mode")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()
	ctx := context.Background()

	sample := contentArraySampleDefault
	if raw := os.Getenv("TEST_CONTENT_ARRAY_SAMPLE"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		require.Greater(t, parsed, 0)
		sample = parsed
	}

	// ★ statement_timeout 单独设：★ 拼在查询里的第二条语句会被 prepared
	//   statement 拒（"cannot insert multiple commands into a prepared
	//   statement"）——★ 这是真库会话族那类大表查询的必备设置。
	_, err = pool.Exec(ctx, "SET statement_timeout = '300s'")
	require.NoError(t, err)

	// ★ 可选：★ 只量【一组指定的 request_id】（★ 每行一个，★ 来自
	//   TEST_CONTENT_ARRAY_REQUEST_IDS 指向的文件）⇒ ⇒ ★ 于是这道门可以
	//   直接回答「某个消费方自己的那批请求，★ 改完之后能读出多少」。
	query := `
		SELECT request_id, response_delta::text
		FROM (
			SELECT request_id, response_delta::text, ts FROM public.session_bodies
			UNION ALL
			SELECT request_id, response_delta::text, ts FROM public.session_bodies_hot
		) t
		WHERE response_delta IS NOT NULL
		ORDER BY ts DESC
		LIMIT $1`
	args := []any{sample}
	if idsPath := os.Getenv("TEST_CONTENT_ARRAY_REQUEST_IDS"); idsPath != "" {
		ids := readRequestIDs(t, idsPath)
		require.NotEmpty(t, ids, "the request-id file must not be empty")
		query = `
			SELECT request_id, response_delta::text
			FROM (
				SELECT request_id, response_delta::text, ts FROM public.session_bodies
				UNION ALL
				SELECT request_id, response_delta::text, ts FROM public.session_bodies_hot
			) t
			WHERE response_delta IS NOT NULL
			  AND request_id = ANY($1::text[])
			ORDER BY ts DESC`
		args = []any{ids}
	}
	rows, err := pool.Query(ctx, query, args...)
	require.NoError(t, err)
	defer rows.Close()

	var (
		total        int
		readableOld  int
		readableNew  int
		regressed    int
		rescued      int
		stillRefused int
		shapes       = map[string]int{}
	)
	for rows.Next() {
		var requestID, raw string
		require.NoError(t, rows.Scan(&requestID, &raw))
		total++

		oldText, oldOK := normalizeControlledResponseOld(&raw)
		newText, newOK := normalizeControlledResponse(&raw)
		if oldOK && oldText != "" {
			readableOld++
			if !newOK || newText == "" {
				regressed++
			}
			continue
		}
		if newOK && newText != "" {
			readableNew++
			rescued++
			continue
		}
		stillRefused++
		shapes[contentArrayShape(&raw)]++
	}
	require.NoError(t, rows.Err())
	require.Greater(t, total, 0, "the sample must not be empty")

	t.Logf("真库读数（最近 %d 行回答）：旧提取器给得出 %d / 给不出 %d；"+
		"新提取器给得出 %d；★ 救回 %d 条；★ 仍然给不出 %d 条；★ 回归 %d 条",
		total, readableOld, total-readableOld, readableNew, rescued,
		stillRefused, regressed)
	for shape, count := range shapes {
		t.Logf("  仍然给不出 —— 形态「%s」：%d 条", shape, count)
	}

	// ① 无回归：★ 旧提取器给得出的，★ 新提取器必须也给得出。
	require.Equal(t, 0, regressed,
		"the change must never take away a response the old reader served")
	// ② 有救援：★ 这次改动在真实数据上确实救回了东西。
	require.Greater(t, rescued, 0,
		"the change must serve at least one response the old reader refused")
	// ③ 恒等式：★ 「给不出的」＝「救回的」＋「仍然给不出的」。
	require.Equal(t, total-readableOld, rescued+stillRefused,
		"every refused response is either rescued or still refused")
}

// contentArrayShape names WHY a response still yields no text.
//
// ★ The point of the classification: ★ 「仍然给不出」不是一个笼统的数 ——
//
//	它要能被拆开，★ 读者才知道其中有没有「有答案却读不出」的那一类
//	（★ 那一类才是这次改动该管、而没管住的）。
func contentArrayShape(raw *string) string {
	if raw == nil {
		return "no-body"
	}
	var array []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(*raw), &array); err == nil {
		hasArray := false
		hasText := false
		for _, message := range array {
			if message.Role != "" && message.Role != "assistant" {
				continue
			}
			if len(message.Content) == 0 {
				continue
			}
			var probe []json.RawMessage
			if err := json.Unmarshal(message.Content, &probe); err == nil {
				hasArray = true
				for _, part := range probe {
					var piece struct {
						Text *string `json:"text"`
					}
					if err := json.Unmarshal(part, &piece); err == nil && piece.Text != nil {
						if *piece.Text != "" {
							hasText = true
						}
					}
				}
			}
		}
		switch {
		case hasArray && hasText:
			return "content-array-with-text-but-refused"
		case hasArray:
			return "content-array-without-text"
		default:
			return "plain-content-or-empty"
		}
	}
	return "not-an-array"
}

// normalizeControlledResponseOld is the pre-change reader, kept HERE (not in
// production code) to measure "before".
//
// ★ It differs from the current reader in exactly one place: it calls
//
//	`controlledString` (plain strings only) where the current one calls
//	`controlledText` (plain strings and content arrays).
func normalizeControlledResponseOld(raw *string) (string, bool) {
	if raw == nil {
		return "", false
	}
	var array []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(*raw), &array); err == nil {
		for _, message := range array {
			if message.Role != "" && message.Role != "assistant" {
				continue
			}
			if content, ok := controlledString(message.Content); ok {
				return content, true
			}
		}
		return "", false
	}
	var document struct {
		Choices []struct {
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Text json.RawMessage `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(*raw), &document); err != nil {
		return "", false
	}
	for _, choice := range document.Choices {
		if choice.Message != nil {
			if content, ok := controlledString(choice.Message.Content); ok {
				return content, true
			}
		}
		if content, ok := controlledString(choice.Text); ok {
			return content, true
		}
	}
	return "", false
}

// readRequestIDs reads one request id per line.
func readRequestIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	ids := make([]string, 0, 64)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}
