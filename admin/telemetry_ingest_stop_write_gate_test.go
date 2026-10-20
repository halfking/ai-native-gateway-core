package admin

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// gateFakeSettingsBackend 是 admin 侧的最小 settings Backend（真实 StoreDB
// 需要 pgxpool；此处按 settings.Backend 接口伪造平台 KV）。
type gateFakeSettingsBackend struct {
	store map[string][]byte
}

func (f *gateFakeSettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *gateFakeSettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (f *gateFakeSettingsBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *gateFakeSettingsBackend) SetTenant(_ string, _ string, _ any) ([]byte, error) {
	return nil, nil
}

// withRequestLogsWriteSetting 把 settings.Global 换成携带真实 storage specs 的
// registry，并把 storage.request_logs_write_enabled 钉在给定值上。
func withRequestLogsWriteSetting(t *testing.T, writeEnabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &gateFakeSettingsBackend{
		store: map[string][]byte{
			"storage.request_logs_write_enabled": []byte(strconv.FormatBool(writeEnabled)),
		},
	})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// TestTelemetryIngestRequestLogStopWriteGate：admin ingest（POST
// /api/telemetry/request-log 消费端）的 S4 停写门控。
//
// ★ 2026-10-20（迁移 890，v1 族真删）：这道门的主体**变了**，但门留下。
//
//	原来：停写门关 ⇒ 只保留 usage_ledger 计费行；门开 ⇒ 额外写
//	      `_request_logs_hot` 主行 + `_request_logs_bodies_hot` 正文
//	      （UPDATE → INSERT..SELECT → UPDATE 三段）。
//	现在：v1 族被 DROP ⇒ 那两条写入**必须**消失（否则对着已删的表执行
//	      ⇒ 42P01 ⇒ 整条 ingest 事务回滚，**连同同事务已写的
//	      usage_ledger 计费行一起丢**）。
//
// ⇒ 门从「门开时写宽表」**反转**为「**任何**门态下都不许再写 v1 表，
// 而门仍然决定**镜像**（文件灾备副本）是否投递」。
//
// 为什么门不能跟着删：S4 停写门（storage.request_logs_write_enabled）是运维
// 的止血开关。它在 890 之后**仍然**有实际作用（镜像投递），而且**必须**继续
// 有效——v1 表退役后，镜像这道灾备副本是本批正文的**唯一**副本。
// ⇒ 删门 = 把「镜像不再受止血开关控制」这件事放过去。
func TestTelemetryIngestRequestLogStopWriteGate(t *testing.T) {
	tests := []struct {
		name         string
		writeOn      bool
		expectMirror bool
	}{
		{name: "gate on mirrors bodies and writes only usage_ledger", writeOn: true, expectMirror: true},
		{name: "gate off keeps only usage_ledger", writeOn: false, expectMirror: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withRequestLogsWriteSetting(t, tc.writeOn)

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			// anyArgSlice 构造 n 个 AnyArg（pgxmock v4 未显式 WithArgs 时
			// 按 0 参数匹配，必须逐条绑定）。
			anyArgSlice := func(n int) []interface{} {
				args := make([]interface{}, n)
				for i := range args {
					args[i] = pgxmock.AnyArg()
				}
				return args
			}

			// 镜像投递计数：停写门现在唯一的实际作用就在这里。
			mirrorCalls := 0
			var mirrorFn BodyMirrorFunc = func(tenantID, requestID, direction string, payload json.RawMessage, at time.Time) {
				mirrorCalls++
			}

			ing := &telemetryIngester{db: mock}
			ing.bodyMirror.Store(&mirrorFn)
			mock.ExpectBegin()
			// ★ PG 侧现在**只有**这一条写入：usage_ledger 计费行。
			// 期望集里没有 v1 的 INSERT/UPDATE ⇒ 一旦生产侧复活它们，
			// pgxmock 会因「未预期的调用」报错 ⇒ 生产侧 return（不 Commit）
			// ⇒ 下面的 ExpectCommit 兑现不了 ⇒ 门红。
			mock.ExpectExec("INSERT INTO usage_ledger_hot").
				WithArgs(anyArgSlice(18)...).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectCommit()

			reqBody := `{"messages":[{"role":"user","content":"hi"}]}`
			respBody := `{"choices":[{"message":{"content":"ok"}}]}`
			ing.persistRequestLog(context.Background(), &requestLogInput{
				RequestID:    "ingest-stop-write-1",
				TenantID:     "tenant-1",
				Success:      true,
				RequestBody:  &reqBody,
				ResponseBody: &respBody,
			})
			require.NoError(t, mock.ExpectationsWereMet())

			// 停写门仍然决定镜像是否投递（req + resp 两条）。
			wantMirror := 0
			if tc.expectMirror {
				wantMirror = 2
			}
			require.Equal(t, wantMirror, mirrorCalls,
				"停写门=%v 时镜像投递次数应为 %d（req/resp 各一）", tc.writeOn, wantMirror)
		})
	}
}

// TestTelemetryIngestHasNoV1Writes 是上面那道门的**源码级**负向断言：
// persistRequestLog 里不得再出现任何已退役 v1 族关系名。
//
// 为什么要有这一条（pgxmock 的期望集不够）：pgxmock 只在「生产侧多调了
// 一个未预期的 Exec」时才报，而生产侧把 Exec 错误吞掉并 return ⇒
// 「多了一条 v1 写入」在这条路径上**可能**只表现为「没有 Commit」⇒
// 依赖副作用的判据是脆的。源码级断言直接打在被测对象上。
//
// ⚠ 阳性对照：同一把尺子必须能认出 v1 形态（否则这条负向断言恒绿）。
// 本包自证门 TestSessionFamilySourceShapeMatchersHaveTeeth 覆盖了尺子本身；
// 这里再就地给一条对照，免得尺子坏了要跨文件才看得出来。
func TestTelemetryIngestHasNoV1Writes(t *testing.T) {
	src, err := os.ReadFile("telemetry.go")
	if err != nil {
		t.Fatalf("read telemetry.go: %v", err)
	}
	// 只取 persistRequestLog 这一个函数（函数级作用域，别把别的函数的
	// 历史 SQL 算进来）。
	i := strings.Index(string(src), "func (t *telemetryIngester) persistRequestLog")
	if i < 0 {
		t.Fatal("persistRequestLog not found")
	}
	rest := string(src)[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}
	// ⚠ **先剥 Go 注释再判**：本函数保留了大量讲 v1 历史背景的注释
	// （「v1 族退役 ⇒ 这两处写入删除」之类），而注释会被下一个人重写。
	// 判据打在注释上 ⇒ 改对代码后门反而更红 ⇒ 那是**反向**的假红。
	// ⇒ 判据只针对**活代码**（与本仓其余判据同款做法）。
	rest = stripGoComments(rest)
	if hits := retiredV1RelationsInSQL(rest); len(hits) > 0 {
		t.Errorf("admin/telemetry.go 的 persistRequestLog 里仍出现已退役 v1 族关系名 %v —— "+
			"v1 族已随 890 被 DROP，写它 = 42P01 = 整条 ingest 事务回滚（连同同事务的计费行）",
			hits)
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态。
	if hits := retiredV1RelationsInSQL("INSERT INTO _request_logs_hot (request_id, ts)"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 形态 —— 这条负向断言已恒绿，判据坏了")
	}
	// 反向对照：canonical 视图不是 v1 族（890 之后它仍存在，只是内容换成会话族）。
	if hits := retiredV1RelationsInSQL("FROM request_logs_with_current_month rl"); len(hits) != 0 {
		t.Errorf("retiredV1RelationsInSQL 把 canonical 视图判成了 v1 族：%v", hits)
	}
}
