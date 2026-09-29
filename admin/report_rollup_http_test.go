package admin

// report_rollup_http_test.go —— 对账报表 HTTP 契约回归（2026-09-29 多维筛选轮）。
//
// 为什么单独一层：前端 `web/src/api/reportrollup.ts` 的类型是**照着 Go 结构体
// 手写的**，不是从真实响应生成的。Go 侧改了 json tag、前端忘了改（或反过来），
// vue-tsc 与 Go 单测都发现不了——页面只会安静地渲染出一堆空格子。
// 本测试拿真库跑一遍端点，把响应 JSON 的键与 TS 声明**逐个对照**。
//
// 只读：不下任何 DELETE，不跑 RollupDay，因此可以直接指向有数据的库。
// 门控：TEST_DATABASE_URL / TEST_DB_URL 未设置时跳过。
//
// ⚠️ 本门与 domains/reportrollup 的破坏性 E2E 需要**状态相反**的库，不能共用一次运行：
//
//	本门（只读，要有数据）        → 指向已跑过日聚合的库，如 llm_gateway
//	TestGrainReport_RealDB_E2E   → 指向一次性空 scratch 库（会 DELETE report_snapshots）
//	用同一个 DSN 跑 ./admin/ ./domains/reportrollup/ 必然一边红：
//	要么这里因空库而红，要么那边因破坏性护栏拦下 llm_gateway 而红。
//	两次都要跑，就跑两遍。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func reportRollupTSPath(t *testing.T) string {
	t.Helper()
	// admin/ 与 web/ 同级
	p := filepath.Join("..", "web", "src", "api", "reportrollup.ts")
	if _, err := os.Stat(p); err != nil {
		// 这里必须 Fatal 而不是 Skip：本测试的全部意义就是拿响应 JSON 逐字段
		// 对照这个 TS 文件。文件找不到时，一条「字段对不上」的门会退化成
		// 什么都不验的 no-op，却照样报 ok。路径是本仓库的固定相对路径
		// （go test 恒以包目录为 cwd），它缺失是仓库坏了，不是本测试不适用。
		t.Fatalf("找不到前端类型文件 %s（本测试的全部前提就是与它逐字段对照）: %v", p, err)
	}
	return p
}

// tsField 是从 TS interface 抽出的一个字段。
type tsField struct {
	name string
	// optional 对应 TS 里的 `?:`——后端带 omitempty 的字段在数据为空时可以
	// 不出现，前端也声明成可选，这类字段不参与「缺失即失败」的断言。
	optional bool
}

// tsInterfaceFields 抽 TS interface 的字段（顶层字段，不含嵌套对象展开）。
func tsInterfaceFields(t *testing.T, src, name string) []tsField {
	t.Helper()
	re := regexp.MustCompile(`(?s)export interface ` + name + `\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("TS 里找不到 interface %s", name)
	}
	fieldRe := regexp.MustCompile(`(?m)^\s{2}([A-Za-z_][A-Za-z0-9_]*)(\??)\s*:`)
	var out []tsField
	for _, mm := range fieldRe.FindAllStringSubmatch(m[1], -1) {
		out = append(out, tsField{name: mm[1], optional: mm[2] == "?"})
	}
	if len(out) == 0 {
		t.Fatalf("interface %s 没解析出字段", name)
	}
	return out
}

func jsonKeys(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func reportRollupGet(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.handleReportRollup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s → %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func TestReportRollup_HTTPContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过 HTTP 契约测试")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect（DSN 已指定，不该静默跳过）: %v", err)
	}
	defer pool.Close()

	// 数据为空时本测试没有意义（会误报「字段缺失」其实只是没数据）。
	//
	// 但「DSN 是显式指定的」和「库是空的」是两回事：操作者点名了一个库却拿到
	// 零行，那是配错库，不是「本测试不适用」。早先这里一律 t.Skip，包级仍然报
	// ok —— 于是一条从未执行过的门看起来像通过了（实测：指向空的
	// llm_gateway_scratch_e2e 时 6 个子测试全部 SKIP，exit 0）。
	// 现在只允许「没给 DSN」这一种跳过；给了 DSN 就必须真的有数据。
	var snaps int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM report_snapshots`).Scan(&snaps); err != nil {
		t.Fatalf("report_snapshots 不可用（DSN 已指定，不该静默跳过）: %v", err)
	}
	if snaps == 0 {
		t.Fatalf(
			"report_snapshots 为空：DSN 已指定却无数据，多半指错了库。"+
				"本测试要求有日聚合结果的库（例如 llm_gateway），空的 scratch 库会让它整条失效。DSN=%s",
			dsn,
		)
	}

	h := NewHandler(pool, "contract-test-secret", nil)
	tsSrc, err := os.ReadFile(reportRollupTSPath(t))
	if err != nil {
		t.Fatalf("read ts: %v", err)
	}
	src := string(tsSrc)

	// 取一个有数据的区间：往回找最近一个有 grain 快照的日子。
	// 上面已断言 report_snapshots 非空；这里连 daily_grain scope 都没有，
	// 说明指向的库没跑迁移 759 —— 那是配错库，必须响，不能悄悄跳过。
	var start, end string
	if err := pool.QueryRow(ctx, `
		SELECT to_char(min(report_date), 'YYYY-MM-DD'), to_char(max(report_date), 'YYYY-MM-DD')
		  FROM report_snapshots WHERE scope = 'daily_grain'`).Scan(&start, &end); err != nil {
		t.Fatalf("没有 daily_grain 快照：库里有 report_snapshots 却无 grain scope，多半是没跑迁移 759。DSN=%s err=%v", dsn, err)
	}
	endT, _ := time.ParseInLocation("2006-01-02", end, time.UTC)
	if endT.After(time.Now().UTC().Truncate(24 * time.Hour)) {
		endT = time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
		end = endT.Format("2006-01-02")
	}
	startT, _ := time.ParseInLocation("2006-01-02", start, time.UTC)
	if startT.After(endT.AddDate(0, 0, -30)) {
		startT = endT.AddDate(0, 0, -30)
		start = startT.Format("2006-01-02")
	}

	t.Run("summary 顶层键与前端 RangeReport 一致", func(t *testing.T) {
		rec := reportRollupGet(t, h, "/api/admin/report-rollup/summary?start="+start+"&end="+end+"&view=internal")
		var envelope struct {
			Report json.RawMessage `json:"report"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if len(envelope.Report) == 0 {
			t.Fatalf("响应缺少 report 字段：%s", rec.Body.String())
		}
		got := jsonKeys(t, envelope.Report)
		var missing []string
		for _, f := range tsInterfaceFields(t, src, "RangeReport") {
			if f.optional {
				continue
			}
			if !got[f.name] {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Fatalf("summary 响应缺必填字段 %v（前端 RangeReport 声明为非可选，"+
				"缺失会让页面安静地渲染出空格子）", missing)
		}
	})

	t.Run("totals 键与前端 ReportTotals 一致", func(t *testing.T) {
		rec := reportRollupGet(t, h, "/api/admin/report-rollup/summary?start="+start+"&end="+end+"&view=provider&detail=daily")
		var envelope struct {
			Report struct {
				Totals json.RawMessage `json:"totals"`
			} `json:"report"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got := jsonKeys(t, envelope.Report.Totals)
		var missing []string
		for _, f := range tsInterfaceFields(t, src, "ReportTotals") {
			if f.optional {
				// 可选字段缺失不报错，但要说明：internal_currency 只在内部
				// 金额非零时出现（omitempty），是真实现象不是契约错误。
				continue
			}
			if !got[f.name] {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Fatalf("totals 缺必填字段 %v（前端会渲染成 undefined）", missing)
		}
	})

	t.Run("分组行含前端要的维度字段", func(t *testing.T) {
		rec := reportRollupGet(t, h, "/api/admin/report-rollup/summary?start="+start+"&end="+end+"&view=provider&detail=daily")
		var envelope struct {
			Report struct {
				Credentials []map[string]json.RawMessage `json:"credentials"`
				APIKeys     []map[string]json.RawMessage `json:"api_keys"`
				ModelTotals []map[string]json.RawMessage `json:"model_totals"`
				DailyModels []map[string]json.RawMessage `json:"daily_models"`
			} `json:"report"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		checks := []struct {
			name string
			rows []map[string]json.RawMessage
			want []string
		}{
			{"credentials", envelope.Report.Credentials, []string{"credential_id", "credential_name", "totals", "error_breakdown"}},
			{"api_keys", envelope.Report.APIKeys, []string{"api_key_id", "api_key_name", "totals", "error_breakdown"}},
			{"model_totals", envelope.Report.ModelTotals, []string{"raw_model_name", "totals"}},
			{"daily_models", envelope.Report.DailyModels, []string{"date", "raw_model_name", "totals"}},
		}
		for _, c := range checks {
			if len(c.rows) == 0 {
				t.Errorf("%s 为空——筛选栏与图表会没有数据", c.name)
				continue
			}
			for _, w := range c.want {
				if _, ok := c.rows[0][w]; !ok {
					t.Errorf("%s 行缺字段 %s", c.name, w)
				}
			}
		}
	})

	t.Run("dimensions 端点", func(t *testing.T) {
		rec := reportRollupGet(t, h, "/api/admin/report-rollup/dimensions?start="+start+"&end="+end+"&view=provider")
		var envelope struct {
			Dimensions struct {
				Providers   []map[string]json.RawMessage `json:"providers"`
				Credentials []map[string]json.RawMessage `json:"credentials"`
				APIKeys     []map[string]json.RawMessage `json:"api_keys"`
				Models      []map[string]json.RawMessage `json:"models"`
				Tenants     []map[string]json.RawMessage `json:"tenants"`
				Persons     []map[string]json.RawMessage `json:"persons"`
			} `json:"dimensions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		d := envelope.Dimensions
		for _, c := range []struct {
			name string
			rows []map[string]json.RawMessage
		}{
			{"providers", d.Providers}, {"credentials", d.Credentials}, {"api_keys", d.APIKeys},
			{"models", d.Models}, {"tenants", d.Tenants}, {"persons", d.Persons},
		} {
			if len(c.rows) == 0 {
				t.Errorf("dimensions.%s 为空", c.name)
				continue
			}
			for _, w := range []string{"key", "requests"} {
				if _, ok := c.rows[0][w]; !ok {
					t.Errorf("dimensions.%s 项缺字段 %s", c.name, w)
				}
			}
		}
	})

	t.Run("六维过滤参数被接受（且双视角通用）", func(t *testing.T) {
		for _, qs := range []string{
			"provider_id=1", "credential_id=11", "api_key_id=100",
			"tenant_id=tenantA", "person=alice", "model=glm-5.3",
		} {
			// provider 视角 + 内部维度：旧实现会 400，这里必须 200。
			rec := reportRollupGet(t, h, "/api/admin/report-rollup/summary?start="+start+"&end="+end+"&view=provider&"+qs)
			_ = rec
			rec2 := reportRollupGet(t, h, "/api/admin/report-rollup/summary?start="+start+"&end="+end+"&view=internal&"+qs)
			_ = rec2
		}
		// 非法值仍应 400。
		for _, qs := range []string{"provider_id=abc", "credential_id=x", "view=bogus"} {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/report-rollup/summary?start="+start+"&end="+end+"&"+qs, nil)
			rec := httptest.NewRecorder()
			h.handleReportRollup(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("非法参数 %q 应 400，实际 %d", qs, rec.Code)
			}
		}
	})

	t.Run("导出 xlsx", func(t *testing.T) {
		rec := reportRollupGet(t, h, "/api/admin/report-rollup/export?start="+start+"&end="+end+"&view=internal")
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "spreadsheetml") {
			t.Fatalf("Content-Type = %q", ct)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "reconciliation_internal") {
			t.Errorf("Content-Disposition = %q", cd)
		}
		body, _ := io.ReadAll(rec.Body)
		if len(body) < 2000 || !strings.HasPrefix(string(body[:2]), "PK") {
			t.Fatalf("导出不是合法 xlsx：%d 字节", len(body))
		}
	})

	t.Run("导出按 ?group= 切「按天×X」那张 sheet", func(t *testing.T) {
		// 缺省必须是 model（老调用方不传 group 也能拿到原来的表）。
		def := exportSheetNames(t, h, start, end, "internal", "")
		if !slices.Contains(def, "按天×模型") {
			t.Errorf("缺省 group 的 sheet 列表里没有「按天×模型」：%v", def)
		}
		// 逐个维度都要真的换掉那张 sheet，而不只是换个名字。
		for _, g := range []string{"provider", "credential", "tenant", "person", "apikey"} {
			got := exportSheetNames(t, h, start, end, "internal", g)
			want := "按天×" + map[string]string{
				"provider": "供应商", "credential": "凭据", "tenant": "租户",
				"person": "用户", "apikey": "apikey",
			}[g]
			if !slices.Contains(got, want) {
				t.Errorf("group=%s 缺 sheet %q，实际：%v", g, want, got)
			}
			if slices.Contains(got, "按天×模型") {
				t.Errorf("group=%s 仍残留「按天×模型」：%v", g, got)
			}
		}
		// 非法值必须 400。静默回退到 model 会让交接口径对不上且无从察觉。
		// 不能用 reportRollupGet——它对任何非 200 直接 t.Fatalf，那正是
		// 本例要断言的状态码。
		bad := httptest.NewRequest(http.MethodGet,
			"/api/admin/report-rollup/export?start="+start+"&end="+end+"&view=internal&group=nope", nil)
		badRec := httptest.NewRecorder()
		h.handleReportRollup(badRec, bad)
		if badRec.Code != http.StatusBadRequest {
			t.Errorf("group=nope 应 400，实得 %d：%s", badRec.Code, badRec.Body.String())
		} else if !strings.Contains(badRec.Body.String(), "unknown group") {
			t.Errorf("400 的错误信息应点名参数非法，实际：%s", badRec.Body.String())
		}
	})
}

// exportSheetNames 走真端点导出，再解出 sheet 名。
func exportSheetNames(t *testing.T, h *Handler, start, end, view, group string) []string {
	t.Helper()
	u := fmt.Sprintf("/api/admin/report-rollup/export?start=%s&end=%s&view=%s", start, end, view)
	if group != "" {
		u += "&group=" + group
	}
	rec := reportRollupGet(t, h, u)
	if rec.Code != http.StatusOK {
		t.Fatalf("export %s: HTTP %d", group, rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "xl/workbook.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open workbook.xml: %v", err)
		}
		defer rc.Close()
		var wb struct {
			Sheets struct {
				Sheet []struct {
					Name string `xml:"name,attr"`
				} `xml:"sheet"`
			} `xml:"sheets"`
		}
		if err := xml.NewDecoder(rc).Decode(&wb); err != nil {
			t.Fatalf("decode workbook.xml: %v", err)
		}
		out := make([]string, 0, len(wb.Sheets.Sheet))
		for _, s := range wb.Sheets.Sheet {
			out = append(out, s.Name)
		}
		return out
	}
	t.Fatal("xlsx 里没有 xl/workbook.xml")
	return nil
}
