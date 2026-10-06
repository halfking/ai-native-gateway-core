package bg

// destructiveGate 的门禁测试（2026-10-06）
//
// 这道门存在的理由是一次真实事故：本文件原先只按 env 变量是否存在放行，
// 于是 TEST_DATABASE_URL 指向 245 时，dropAll825Objects 把
// public.ursm_node_snapshot_min 连 CASCADE 删掉，453/818 的台账却原封不动。
// 见 bg/ursm_825_realdb_test.go 中 destructiveGate 的注释。
//
// ⚠️ 本文件**不连任何数据库**——它只测判定逻辑。真正的破坏性用例在
// ursm_825_realdb_test.go 里，且那些用例在没有 TEST_DATABASE_URL 时会 skip。

import (
	"strings"
	"testing"
)

func TestDestructiveGateRefusesNonTestDatabase(t *testing.T) {
	// 事故当天用的就是第 2 行那种形状：URL 形态、库名里没有 test。
	cases := []struct {
		name    string
		dsn     string
		wantErr bool
	}{
		{
			name:    "URL 形态 · 测试库放行",
			dsn:     "postgres://llm_gateway:pw@127.0.0.1:5432/llm_gateway_test?sslmode=disable",
			wantErr: false,
		},
		{
			name:    "URL 形态 · 非测试库拒绝（245 的真实形状）",
			dsn:     "postgres://llm_gateway:pw@10.0.0.1:5432/llm_gateway?sslmode=disable",
			wantErr: true,
		},
		{
			name:    "URL 形态 · 库名大小写混写也应放行",
			dsn:     "postgres://u:p@h:5432/LLM_Gateway_TEST?sslmode=disable",
			wantErr: false,
		},
		{
			name:    "关键字形态 · 测试库放行",
			dsn:     "host=127.0.0.1 port=5432 dbname=llm_gateway_test user=u password=p",
			wantErr: false,
		},
		{
			name:    "关键字形态 · 非测试库拒绝",
			dsn:     "host=127.0.0.1 port=5432 dbname=llm_gateway user=u password=p",
			wantErr: true,
		},
		{
			name:    "关键字形态 · dbname 带引号",
			dsn:     "host=127.0.0.1 dbname='ursm_825_scratch_test' user=u",
			wantErr: false,
		},
		{
			name:    "解析不出库名 → 拒绝（宁可拒绝也不猜）",
			dsn:     "not-a-dsn-at-all",
			wantErr: true,
		},
		{
			name:    "空 DSN → 拒绝",
			dsn:     "",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("URSM_830_ALLOW_ANY_DB", "")
			err := destructiveGate(tc.dsn)
			if tc.wantErr && err == nil {
				t.Fatalf("dsn=%q 应当被拒绝，却放行了 —— 这道门形同虚设，"+
					"指向生产库的 TEST_DATABASE_URL 会直接 DROP 生产表", tc.dsn)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("dsn=%q 应当放行，却被拒：%v", tc.dsn, err)
			}
		})
	}
}

// 逃生阀必须真的有效，否则操作人被门挡住时的唯一出路是改门。
func TestDestructiveGateEscapeHatchWorks(t *testing.T) {
	t.Setenv("URSM_830_ALLOW_ANY_DB", "1")
	if err := destructiveGate("postgres://u:p@h:5432/llm_gateway?sslmode=disable"); err != nil {
		t.Fatalf("URSM_830_ALLOW_ANY_DB=1 时应当无条件放行，却仍被拒：%v", err)
	}
}

// 拒绝理由里必须带库名，否则操作人无法判断自己是不是被误伤。
func TestDestructiveGateRejectionNamesTheDatabase(t *testing.T) {
	t.Setenv("URSM_830_ALLOW_ANY_DB", "")
	err := destructiveGate("postgres://u:p@h:5432/llm_gateway?sslmode=disable")
	if err == nil {
		t.Fatal("应当被拒绝")
	}
	if !strings.Contains(err.Error(), "llm_gateway\"") && !strings.Contains(err.Error(), "llm_gateway") {
		t.Fatalf("拒绝理由里没有库名，操作人无法判断是否误伤：%v", err)
	}
}

// redactDSN 的作用是让拒绝日志可以安全贴出来——口令绝不能出现。
func TestRedactDSNRemovesCredentials(t *testing.T) {
	cases := []struct{ in, mustNotContain, mustContain string }{
		{
			in:             "postgres://llm_gateway:sup3rs3cret@h:5432/llm_gateway?sslmode=disable",
			mustNotContain: "sup3rs3cret",
			mustContain:    "llm_gateway",
		},
		{
			in:             "host=h dbname=d user=u password=hunter2",
			mustNotContain: "hunter2",
			mustContain:    "dbname=d",
		},
	}
	for _, tc := range cases {
		got := redactDSN(tc.in)
		if strings.Contains(got, tc.mustNotContain) {
			t.Errorf("redactDSN(%q) = %q —— 口令泄漏到日志里了", tc.in, got)
		}
		if !strings.Contains(got, tc.mustContain) {
			t.Errorf("redactDSN(%q) = %q —— 库名/host 被抹掉了，操作人无法判断指向哪", tc.in, got)
		}
	}
}

func TestDsnDatabaseName(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h:5432/mydb?sslmode=disable": "mydb",
		"postgresql://u:p@h:5432/mydb":               "mydb",
		"host=h dbname=mydb user=u":                  "mydb",
		"host=h dbname='my db' user=u":               "my db",
		"garbage":                                    "",
		"":                                           "",
	}
	for in, want := range cases {
		if got := dsnDatabaseName(in); got != want {
			t.Errorf("dsnDatabaseName(%q) = %q, want %q", in, got, want)
		}
	}
}
