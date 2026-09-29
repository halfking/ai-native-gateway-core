package reportrollup

// grainreport_e2e_test.go —— 多维读面真库回归（2026-09-29 多维筛选轮）。
//
// 路径：真 PG（scratch schema）→ 建 report_snapshots（759 后形状）→ 直接
// 灌 grain / 旧口径快照行 → BuildGrainReport / LoadDimensionOptions /
// BuildGrainWorkbookBytes 全链路断言。
//
// 为什么直接灌快照而不走 RollupDay：RollupDay 的 SQL 正确性由
// realdb_e2e_test.go 与本文件共用；本文件要钉的是**读面**的三条不变量，
// 用手写快照行能把每条不变量做成最小可复现样本：
//   ① Σ任一维度分组 = 区间总计（含未落定档）；
//   ② 任意维度组合过滤后 ① 仍成立（旧读面只在单维过滤时成立）；
//   ③ 区间内只有旧口径快照的日期回落计入总计与按天，不进维度分组。
//
// 门控：TEST_DATABASE_URL / TEST_DB_URL 未设置时跳过。本测试只在
// scratch schema 内写读，不碰 public。

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// xlsxSheetNames 读出工作簿的 sheet 名（走真实的 zip + workbook.xml，
// 不是复用生产代码的 sheet 构造，否则「构造和断言用同一份逻辑」会让
// 这条门恒绿）。
func xlsxSheetNames(t *testing.T, b []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("open xlsx zip: %v", err)
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

// xlsxSheet3Text 取第 3 张 sheet 的全部文本（sheet 顺序由构造固定：汇总/
// 按天/按天×X/按天明细，所以第 3 张就是那张随 group 变的）。
func xlsxSheet3Text(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("open xlsx zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "xl/worksheets/sheet3.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open sheet3: %v", err)
		}
		defer rc.Close()
		raw, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read sheet3: %v", err)
		}
		return string(raw)
	}
	t.Fatal("xlsx 里没有 xl/worksheets/sheet3.xml")
	return ""
}

func grainE2EDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库 E2E")
	}
	return dsn
}

// grainFixture 一行最细粒度快照的灌数参数。
type grainFixture struct {
	date        string
	provider    *int64
	credential  *int64
	apiKey      *int64
	tenant      string
	person      string
	model       string
	requests    int64
	success     int64
	breakdown   map[string]int64
	legacyScope Scope // 空 = daily_grain
	legacyKey   string
}

func TestGrainReport_RealDB_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const schema = "report_grain_e2e"
	// scratch schema 而不是整库：既不碰 public 的生产形状数据，也不用
	// 「清空 report_snapshots」这种会毁掉共享开发库的做法。
	// search_path 必须挂在 AfterConnect 上——pgxpool 的连接是复用的，
	// 在某条连接上执行一次 SET 下一条连接就失效了。
	cfg, err := pgxpool.ParseConfig(grainE2EDSN(t))
	if err != nil {
		// DSN 是显式指定的却用不了 = 配错，不是「本测试不适用」。
		// 早先这里 Skip，配错库时整条 E2E 静默消失、包级仍报 ok。
		t.Fatalf("parse dsn（DSN 已指定，不该静默跳过）: %v", err)
	}
	// 必须在**装钩子之前**用一条独立连接把原 search_path 抓下来。
	//
	// 首版断言是在池子建好之后才去 pg_settings 读 search_path —— 那时
	// AfterConnect 已经改过了，读到的是**改后**的值，拿它当「原值」比较恒等，
	// 于是把钩子改成覆盖式也照样绿。基线绿 + 变异绿 = 门没有守卫力。
	rawConn, err := pgx.Connect(ctx, grainE2EDSN(t))
	if err != nil {
		t.Fatalf("connect (capture baseline search_path): %v", err)
	}
	var origPath string
	if err := rawConn.QueryRow(ctx, `SHOW search_path`).Scan(&origPath); err != nil {
		rawConn.Close(ctx)
		t.Fatalf("show baseline search_path: %v", err)
	}
	rawConn.Close(ctx)

	// search_path 必须挂在 AfterConnect 上——pgxpool 的连接是复用的，某条
	// 连接上执行一次 SET，下一条连接就失效了。
	//
	// 注意要**追加**而不是覆盖原 search_path：本库装了 citus_columnar，
	// 其 ddl_command_end 事件触发器要解析 columnar_insert_only_parents()；
	// 把 search_path 收窄成只有 scratch schema 会让函数解析失败，建表直接
	// 42883（真库实测踩过）。下面的断言让这条规则在**没有**那个扩展的
	// 机器上也会红，不靠扩展是否存在来兜底。
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		var orig string
		if err := c.QueryRow(ctx, `SHOW search_path`).Scan(&orig); err != nil {
			return err
		}
		_, err := c.Exec(ctx, "SET search_path TO "+schema+", "+orig)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect（DSN 已指定，不该静默跳过）: %v", err)
	}
	defer pool.Close()

	// AfterConnect 的不变量本身要验：scratch schema 生效 **且** 钩子之前抓到的
	// 原 search_path 仍在。比较的是「独立连接在装钩子前」读到的 origPath，
	// 不是 pg_settings（那是改后的值，拿它比恒等）。
	var gotPath string
	if err := pool.QueryRow(ctx, `SHOW search_path`).Scan(&gotPath); err != nil {
		t.Fatalf("show search_path: %v", err)
	}
	head := strings.Split(gotPath, ",")[0]
	if head != schema {
		t.Fatalf("scratch schema 未生效：search_path 首项 = %q，期望 %q（整串：%s）", head, schema, gotPath)
	}
	for _, want := range strings.Split(origPath, ",") {
		want = strings.TrimSpace(strings.Trim(want, `"`))
		if want == "" {
			continue
		}
		if !strings.Contains(gotPath, want) {
			t.Errorf("AfterConnect 把 search_path 收窄了：装钩子前的原值里有 %q，"+
				"现值里没有了（现值：%s）。必须**追加**原值——citus_columnar 的"+
				" ddl_command_end 触发器要解析 columnar_insert_only_parents()，"+
				" 收窄后建表直接 42883（真库实测踩过）；在没装该扩展的机器上"+
				" 这条断言也要能红。", want, gotPath)
		}
	}

	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := pool.Exec(ctx, grainE2ETableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}

	day := func(offset int) string {
		return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02")
	}
	p1, c1, k1 := int64(1), int64(11), int64(101)
	p2, c2, k2 := int64(2), int64(22), int64(202)

	fixtures := []grainFixture{
		// d-3：完整的六维组合，四行覆盖两个供应商 × 两个凭据 × 两个 apikey。
		{date: day(-3), provider: &p1, credential: &c1, apiKey: &k1, tenant: "t1", person: "alice", model: "gpt-x", requests: 100, success: 90, breakdown: map[string]int64{"timeout": 10}},
		{date: day(-3), provider: &p1, credential: &c2, apiKey: &k1, tenant: "t1", person: "bob", model: "gpt-y", requests: 50, success: 50},
		{date: day(-3), provider: &p2, credential: &c2, apiKey: &k2, tenant: "t2", person: "alice", model: "gpt-x", requests: 25, success: 20, breakdown: map[string]int64{"timeout": 3, "rate_limit_exceeded": 2}},
		// 未落定：未路由到供应商的限流失败（provider/credential 均 NULL，
		// apikey 仍可落定）。这行是「Σ分组=总计」最容易漏的一档。
		{date: day(-3), provider: nil, credential: nil, apiKey: &k1, tenant: "t1", person: "alice", model: "", requests: 200, success: 0, breakdown: map[string]int64{"rate_limit_exceeded": 200}},
		// d-2：另一天，同样两组。
		{date: day(-2), provider: &p1, credential: &c1, apiKey: &k1, tenant: "t1", person: "alice", model: "gpt-x", requests: 10, success: 10},
		{date: day(-2), provider: &p2, credential: &c2, apiKey: &k2, tenant: "t2", person: "bob", model: "gpt-y", requests: 5, success: 4, breakdown: map[string]int64{"timeout": 1}},
		// d-1：只有旧口径 daily_total（模拟迁移落地前的一天）。
		{date: day(-1), requests: 77, success: 70, breakdown: map[string]int64{"provider_error": 7}, legacyScope: ScopeDailyTotal, legacyKey: "all"},
	}
	insertGrainFixtures(t, ctx, pool, fixtures)

	start, _ := time.ParseInLocation("2006-01-02", day(-3), time.UTC)
	end, _ := time.ParseInLocation("2006-01-02", day(-1), time.UTC)
	names := Names{
		Providers:   map[int64]string{1: "供应商一", 2: "供应商二"},
		Credentials: map[int64]string{11: "凭据一", 22: "凭据二"},
		APIKeys:     map[int64]string{101: "apikey-a", 202: "apikey-b"},
	}

	t.Run("汇总口径下 Σ各维度分组 = 总计", func(t *testing.T) {
		rep, err := BuildGrainReport(ctx, pool, start, end, ViewProvider, GrainFilter{}, names, false)
		if err != nil {
			t.Fatalf("BuildGrainReport: %v", err)
		}
		if rep.Totals.RequestCount != 100+50+25+200+10+5+77 {
			t.Fatalf("totals.req = %d, want 467", rep.Totals.RequestCount)
		}
		// 200 条未落定的限流失败同样计入失败数（未落定 ≠ 不算失败）。
		if rep.Totals.ErrorCount != 10+5+200+1+7 {
			t.Fatalf("totals.err = %d, want 223", rep.Totals.ErrorCount)
		}
		// 200（未落定限流）+ 2（供应商二那行）= 202；不是行请求数 225。
		if rep.TopErrorKind != "rate_limit_exceeded" || rep.TopErrorCount != 202 {
			t.Fatalf("top error = %q/%d", rep.TopErrorKind, rep.TopErrorCount)
		}
		// grain 覆盖 2 天 + 旧口径 1 天。
		if len(rep.Coverage.GrainDates) != 2 || len(rep.Coverage.LegacyDates) != 1 {
			t.Fatalf("coverage = %+v", rep.Coverage)
		}
		if rep.Source != "mixed" {
			t.Fatalf("source = %q, want mixed", rep.Source)
		}
		// 维度分组只覆盖 grain 的两日（390 条），旧口径那天进不了维度分解。
		const grainReq = 100 + 50 + 25 + 200 + 10 + 5
		sumID := func(m map[int64]int64) int64 {
			var s int64
			for _, v := range m {
				s += v
			}
			return s
		}
		provIDs := map[int64]int64{}
		for _, p := range rep.Providers {
			provIDs[p.ProviderID] = p.Totals.RequestCount
		}
		if len(rep.Providers) != 3 || provIDs[UnassignedID] != 200 {
			t.Fatalf("providers = %+v（应含未落定档 200）", provIDs)
		}
		credIDs := map[int64]int64{}
		for _, c := range rep.Credentials {
			credIDs[c.CredentialID] = c.Totals.RequestCount
		}
		credSum := sumID(credIDs)
		if len(credIDs) != 3 {
			t.Fatalf("credentials 应含两个真实凭据 + 未落定档：%+v", credIDs)
		}
		modelSum := int64(0)
		for _, m := range rep.ModelTotals {
			modelSum += m.Totals.RequestCount
		}
		keySum := int64(0)
		for _, k := range rep.APIKeys {
			keySum += k.Totals.RequestCount
		}
		if provIDs[1]+provIDs[2]+provIDs[UnassignedID] != grainReq {
			t.Fatalf("Σproviders = %d, want %d", provIDs[1]+provIDs[2]+provIDs[UnassignedID], grainReq)
		}
		if credSum != grainReq {
			t.Fatalf("Σcredentials = %d, want %d", credSum, grainReq)
		}
		if modelSum != grainReq {
			t.Fatalf("ΣmodelTotals = %d, want %d", modelSum, grainReq)
		}
		if keySum != grainReq {
			t.Fatalf("ΣapiKeys = %d, want %d", keySum, grainReq)
		}
		// 供应商×模型行也应自洽。
		pm := int64(0)
		for _, m := range rep.Models {
			pm += m.Totals.RequestCount
		}
		if pm != grainReq {
			t.Fatalf("ΣproviderModels = %d, want %d", pm, grainReq)
		}
		// 按天序列覆盖整个区间（缺失日补 0），且 Σ天 = 总计。
		if len(rep.Days) != 3 {
			t.Fatalf("days = %d, want 3", len(rep.Days))
		}
		var daySum int64
		for _, d := range rep.Days {
			daySum += d.Totals.RequestCount
		}
		if daySum != rep.Totals.RequestCount {
			t.Fatalf("Σdays = %d, totals = %d", daySum, rep.Totals.RequestCount)
		}
		// 图表数据面：按天×模型
		if len(rep.DailyModels) < 3 {
			t.Fatalf("dailyModels = %d 行, want ≥3", len(rep.DailyModels))
		}
	})

	t.Run("任意维度组合过滤后 Σ分组仍 = 总计", func(t *testing.T) {
		// 这正是旧读面做不到的：边缘 scope 只能单维折叠，多维过滤后总计与
		// 分组对不上。
		rep, err := BuildGrainReport(ctx, pool, start, end, ViewProvider,
			GrainFilter{ProviderID: &p1, CredentialID: &c1, TenantID: "t1"}, names, true)
		if err != nil {
			t.Fatalf("BuildGrainReport: %v", err)
		}
		if rep.Totals.RequestCount != 100+10 {
			t.Fatalf("过滤后 totals.req = %d, want 110", rep.Totals.RequestCount)
		}
		if len(rep.Providers) != 1 || rep.Providers[0].ProviderID != 1 {
			t.Fatalf("providers = %+v", rep.Providers)
		}
		if len(rep.DailyProviders) != 2 {
			t.Fatalf("dailyProviders = %d 行, want 2", len(rep.DailyProviders))
		}
		if len(rep.DailyCredentials) != 2 {
			t.Fatalf("dailyCredentials = %d 行, want 2", len(rep.DailyCredentials))
		}
		if len(rep.DailyAPIKeys) != 2 || len(rep.DailyTenants) != 2 || len(rep.DailyPersons) != 2 {
			t.Fatalf("明细维度行数异常: keys=%d tenants=%d persons=%d",
				len(rep.DailyAPIKeys), len(rep.DailyTenants), len(rep.DailyPersons))
		}
		// 模型过滤叠加。
		rep2, err := BuildGrainReport(ctx, pool, start, end, ViewProvider,
			GrainFilter{ProviderID: &p1, Model: "gpt-x"}, names, false)
		if err != nil {
			t.Fatalf("BuildGrainReport(model): %v", err)
		}
		if rep2.Totals.RequestCount != 110 {
			t.Fatalf("provider+model 过滤 totals = %d, want 110", rep2.Totals.RequestCount)
		}
		if len(rep2.ModelTotals) != 1 || rep2.ModelTotals[0].RawModelName != "gpt-x" {
			t.Fatalf("modelTotals = %+v", rep2.ModelTotals)
		}
		// 用户维度过滤。
		rep3, err := BuildGrainReport(ctx, pool, start, end, ViewProvider,
			GrainFilter{Person: "bob"}, names, false)
		if err != nil {
			t.Fatalf("BuildGrainReport(person): %v", err)
		}
		if rep3.Totals.RequestCount != 50+5 {
			t.Fatalf("person 过滤 totals = %d, want 55", rep3.Totals.RequestCount)
		}
	})

	t.Run("旧口径日期只进总计与按天", func(t *testing.T) {
		// 只取那一天：既无 grain 行。此时 source=legacy，维度分组应为空，
		// 总计 = 该日 daily_total。
		d := start.AddDate(0, 0, 2)
		rep, err := BuildGrainReport(ctx, pool, d, d, ViewProvider, GrainFilter{}, names, false)
		if err != nil {
			t.Fatalf("BuildGrainReport(legacy day): %v", err)
		}
		if rep.Source != "legacy" {
			t.Fatalf("source = %q, want legacy", rep.Source)
		}
		if rep.Totals.RequestCount != 77 {
			t.Fatalf("legacy totals = %d, want 77", rep.Totals.RequestCount)
		}
		if len(rep.Providers) != 0 || len(rep.Credentials) != 0 || len(rep.APIKeys) != 0 {
			t.Fatalf("旧口径不该产出维度分组：prov=%d cred=%d key=%d",
				len(rep.Providers), len(rep.Credentials), len(rep.APIKeys))
		}
		if len(rep.Days) != 1 || rep.Days[0].Totals.RequestCount != 77 {
			t.Fatalf("legacy days = %+v", rep.Days)
		}
	})

	t.Run("维度候选不含未落定且按请求数降序", func(t *testing.T) {
		dims, err := LoadDimensionOptions(ctx, pool, ViewProvider, start, end)
		if err != nil {
			t.Fatalf("LoadDimensionOptions: %v", err)
		}
		if len(dims.Credentials) != 2 {
			t.Fatalf("credentials = %+v", dims.Credentials)
		}
		for _, o := range dims.Credentials {
			if o.Key == "" || o.Requests <= 0 {
				t.Fatalf("候选含空键或零请求：%+v", o)
			}
		}
		if dims.Credentials[0].Requests < dims.Credentials[1].Requests {
			t.Fatalf("候选未按请求数降序：%+v", dims.Credentials)
		}
		if len(dims.Models) == 0 || len(dims.Tenants) != 2 || len(dims.Persons) != 2 {
			t.Fatalf("候选维度不齐：models=%d tenants=%d persons=%d",
				len(dims.Models), len(dims.Tenants), len(dims.Persons))
		}
		// 空模型名（未路由到任何模型的失败行）不进候选——前端没法拿它做筛选。
		if len(dims.Models) != 2 {
			t.Fatalf("models = %+v（应只有 gpt-x/gpt-y，空模型名不入候选）", dims.Models)
		}
		// 未落定不是可选项：筛选器里没有「provider = 空」这种用法。
		for _, o := range dims.Providers {
			if o.Key == "" {
				t.Fatal("供应商候选出现空键")
			}
		}
	})

	t.Run("导出多 sheet 工作簿", func(t *testing.T) {
		rep, err := BuildGrainReport(ctx, pool, start, end, ViewProvider, GrainFilter{}, names, true)
		if err != nil {
			t.Fatalf("BuildGrainReport: %v", err)
		}
		b, err := BuildGrainWorkbookBytes(rep, GrainGroupModel)
		if err != nil {
			t.Fatalf("BuildGrainWorkbookBytes: %v", err)
		}
		if len(b) < 2000 {
			t.Fatalf("xlsx 过小：%d 字节", len(b))
		}
	})

	t.Run("导出按天×X sheet 跟着 group 参数走", func(t *testing.T) {
		rep, err := BuildGrainReport(ctx, pool, start, end, ViewProvider, GrainFilter{}, names, true)
		if err != nil {
			t.Fatalf("BuildGrainReport: %v", err)
		}
		// 夹具里各维度用可区分的取值（模型 gpt-x、租户 t1、用户 alice、
		// 供应商「供应商一」…），这样「sheet 名字对了但内容还是模型」这种
		// 假开关会被内容断言抓住。只断言 sheet 名字是不够的：改个名照样能过。
		cases := []struct {
			group  GrainGroup
			mustIn string // 该维度的取值必须出现在 sheet 里
		}{
			{GrainGroupModel, "gpt-x"},
			{GrainGroupProvider, "供应商一"},
			{GrainGroupCredential, "凭据一"},
			{GrainGroupTenant, "t1"},
			{GrainGroupPerson, "alice"},
			{GrainGroupAPIKey, "apikey-a"},
		}
		for _, c := range cases {
			b, err := BuildGrainWorkbookBytes(rep, c.group)
			if err != nil {
				t.Fatalf("%s: BuildGrainWorkbookBytes: %v", c.group, err)
			}
			sheets := xlsxSheetNames(t, b)
			if !slices.Contains(sheets, c.group.SheetName()) {
				t.Errorf("group=%s 缺 sheet %q，实际 sheets=%v", c.group, c.group.SheetName(), sheets)
			}
			if c.group != GrainGroupModel && slices.Contains(sheets, GrainGroupModel.SheetName()) {
				t.Errorf("group=%s 仍残留模型 sheet %q", c.group, GrainGroupModel.SheetName())
			}
			// sheet 数量恒为 4：换维度不改变结构，只换口径。
			if len(sheets) != 4 {
				t.Errorf("group=%s sheet 数=%d，期望 4：%v", c.group, len(sheets), sheets)
			}
			// 关键：第 3 张 sheet（构造顺序固定）必须真的含该维度的取值。
			body := xlsxSheet3Text(t, b)
			if !strings.Contains(body, c.mustIn) {
				t.Errorf("group=%s 的「%s」sheet 里找不到该维度取值 %q——sheet 只是改了名字，内容仍是别的维度",
					c.group, c.group.SheetName(), c.mustIn)
			}
		}
	})

	t.Run("group 参数非法时拒绝而不是静默回退", func(t *testing.T) {
		if _, err := ParseGrainGroup("tenantX"); err == nil {
			t.Fatal("ParseGrainGroup(tenantX) 应报错，静默回退会让交接口径对不上")
		}
		for _, ok := range []string{"", "model", "PROVIDER", " credential ", "tenant", "person", "apikey"} {
			if _, err := ParseGrainGroup(ok); err != nil {
				t.Errorf("ParseGrainGroup(%q) 不该报错: %v", ok, err)
			}
		}
		if g, _ := ParseGrainGroup(""); g != GrainGroupModel {
			t.Errorf("空串应默认 model，实得 %q", g)
		}
	})
}

// grainE2ETableDDL 是 report_snapshots 在 759 之后的最小形状（列定义与
// SSOT 一致；只保留本测试用到的列）。
const grainE2ETableDDL = `
CREATE TABLE report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,
    scope_key           TEXT NOT NULL,
    report_date         DATE NOT NULL,
    raw_model_name      TEXT NOT NULL DEFAULT '',
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,
    tenant_id           TEXT,
    credential_id       BIGINT,
    api_key_id          BIGINT,
    person              TEXT,
    credits_charged     BIGINT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
)`

func insertGrainFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixtures []grainFixture) {
	t.Helper()
	for _, f := range fixtures {
		scope := f.legacyScope
		if scope == "" {
			scope = ScopeDailyGrain
		}
		scopeKey := f.legacyKey
		if scopeKey == "" {
			scopeKey = grainScopeKey(f.provider, f.credential, f.apiKey, f.tenant, f.person)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO report_snapshots (
			    scope, scope_key, report_date, raw_model_name,
			    request_count, success_count, error_count,
			    input_tokens, output_tokens, error_kind_breakdown,
			    estimated_cost_cents, currency, provider_id, tenant_id,
			    credential_id, api_key_id, person, credits_charged
			) VALUES (
			    $1, $2, $3::date, $4,
			    $5::bigint, $6::bigint, $5::bigint - $6::bigint,
			    $5::bigint * 10, $5::bigint * 2, $7::text::jsonb,
			    $5::bigint * 10, 'USD', $8, $9, $10, $11, $12, $13
			)`,
			string(scope), scopeKey, f.date, f.model,
			f.requests, f.success, grainBreakdownJSON(t, f.breakdown),
			f.provider, grainNullable(f.tenant), f.credential, f.apiKey, grainNullable(f.person), f.requests,
		); err != nil {
			t.Fatalf("insert fixture %+v: %v", f, err)
		}
	}
}

func grainBreakdownJSON(t *testing.T, m map[string]int64) string {
	t.Helper()
	if m == nil {
		return "{}"
	}
	out := "{"
	first := true
	for k, v := range m {
		if !first {
			out += ","
		}
		first = false
		out += `"` + k + `":` + grainItoa(v)
	}
	return out + "}"
}

func grainItoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func grainNullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── 唯一真跑迁移本体的门 ────────────────────────────────────────────────────
//
// 为什么要它：上面整套 E2E 建表用的是 grainE2ETableDDL（手写的 759 之后形状），
// **从不执行 759 迁移文件本身**。于是迁移 SQL 里一个语法错误、一个拼错的
// 列名、一个没生效的语句，都不会有任何测试发现——门全绿，迁移却是坏的。
// 本门把 759 的原文喂给真库执行，并核对结果。
//
// 用 e2eDSN（带 guardDestructiveE2E 护栏）而不是 grainE2EDSN：759 的 ALTER 写死
// 作用于 public.report_snapshots，会动到真实表，必须受那道护栏保护。
func TestMigration759_AppliesToRealScratchDB(t *testing.T) {
	dsn := e2eDSN(t) // ← 带破坏性护栏的那一个
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 相对本包目录（go test 的 cwd = 包目录），不是 startup 目录。
	mig, err := os.ReadFile(filepath.Join("..", "..", "sql", "migrations", "startup",
		"759_report_snapshots_grain_dims.sql"))
	if err != nil {
		t.Fatalf("read migration 759: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 全程在一个事务里跑，最后 ROLLBACK——PG 的 DDL 是事务性的，于是本测试
	// 对 public.report_snapshots 的 DROP/CREATE **不留任何痕迹**。
	//
	// 不用事务的话它会与 realdb_e2e_test.go 的 TestReportRollup_RealDB_E2E
	// 互相踩：两者都重建 public.report_snapshots，而形状不同（本测试用
	// pre759ReportSnapshotsDDL，另一个要 cache_read_tokens 等列），
	// 谁后跑谁坏——一个顺序相关的偶发失败，比没有这个测试更糟。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 恢复到 **759 之前** 的形状（746 之后：有 credits/latency/tenant_id TEXT，
	// 无三列）。缺这三列正是本测试的起点。
	if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS public.report_snapshots CASCADE`); err != nil {
		t.Fatalf("drop report_snapshots: %v", err)
	}
	if _, err := tx.Exec(ctx, pre759ReportSnapshotsDDL); err != nil {
		t.Fatalf("create pre-759 report_snapshots: %v", err)
	}

	// 先记下迁移前没有这三列，否则「跑完还在」不算证据。
	for _, c := range []string{"credential_id", "api_key_id", "person"} {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema='public' AND table_name='report_snapshots' AND column_name=$1`, c).Scan(&n); err != nil {
			t.Fatalf("probe column %s: %v", c, err)
		}
		if n != 0 {
			t.Fatalf("前置条件不成立：迁移前 %s 就已存在，测不到新增", c)
		}
	}

	apply := func(round string) {
		if _, err := tx.Exec(ctx, string(mig)); err != nil {
			t.Fatalf("应用 759 失败（%s）：%v", round, err)
		}
	}
	apply("首跑")

	for _, c := range []string{"credential_id", "api_key_id", "person"} {
		var typ string
		if err := tx.QueryRow(ctx, `SELECT data_type FROM information_schema.columns
			WHERE table_schema='public' AND table_name='report_snapshots' AND column_name=$1`, c).Scan(&typ); err != nil {
			t.Errorf("迁移后查不到列 %s：%v", c, err)
		} else if c != "person" && typ != "bigint" {
			t.Errorf("列 %s 类型 = %q，期望 bigint", c, typ)
		} else if c == "person" && typ != "text" {
			t.Errorf("列 %s 类型 = %q，期望 text", c, typ)
		}
	}
	for _, idx := range []string{
		"idx_report_snapshots_grain_date",
		"idx_report_snapshots_internal_grain_date",
		"idx_report_snapshots_credential_date",
		"idx_report_snapshots_api_key_date",
	} {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
			WHERE schemaname='public' AND tablename='report_snapshots' AND indexname=$1`, idx).Scan(&n); err != nil {
			t.Errorf("查索引 %s: %v", idx, err)
		} else if n != 1 {
			t.Errorf("索引 %s 不存在（pg_indexes 命中 %d 条）——迁移里的 CREATE INDEX 没生效", idx, n)
		}
	}

	// 迁移自称可重入：再跑一次必须仍然成功且不重复加列。
	apply("重跑（幂等）")
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='report_snapshots'
		  AND column_name IN ('credential_id','api_key_id','person')`).Scan(&total); err != nil {
		t.Fatalf("重跑后统计: %v", err)
	}
	if total != 3 {
		t.Errorf("重跑后三列应恰好 3 个，实得 %d —— 迁移不可重入", total)
	}
}

// pre759ReportSnapshotsDDL 是 759 **之前**的表形状（746 之后）。
// 缺这三列正是本测试的起点。
const pre759ReportSnapshotsDDL = `
CREATE TABLE public.report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,
    scope_key           TEXT NOT NULL,
    report_date         DATE NOT NULL,
    raw_model_name      TEXT NOT NULL DEFAULT '',
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    provider_id         BIGINT,
    tenant_id           TEXT,
    credits_charged     BIGINT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
)`
