package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/stretchr/testify/require"
)

func TestSessionWriteSafetyRejectsMissingCanonicalSource(t *testing.T) {
	previous := settings.Global
	settings.Global = settings.NewRegistry()
	t.Cleanup(func() { settings.Global = previous })
	settings.Init(nil)
	for _, spec := range append(settings.StorageSpecs(), settings.SessionsV2Specs()...) {
		settings.Global.MustRegisterSpec(spec)
	}
	for _, v1 := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, shadow := range []bool{false, true} {
				for _, pg := range []bool{false, true} {
					t.Run(fmt.Sprintf("v1=%t/v2=%t/shadow=%t/pg=%t", v1, enabled, shadow, pg), func(t *testing.T) {
						t.Setenv(settings.EnvNameAuto(settings.KeyRequestLogsWriteEnabled), fmt.Sprint(v1))
						t.Setenv("LLM_GATEWAY_SESSIONS_V2_ENABLED", fmt.Sprint(enabled))
						t.Setenv("LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE", fmt.Sprint(shadow))
						err := validateSessionWriteSafety(pg)
						wantError := !v1 && !(pg && enabled && shadow)
						if (err != nil) != wantError {
							t.Fatalf("error=%v, unsafe=%v", err, wantError)
						}
					})
				}
			}
		}
	}
}

func TestSessionWriteSafetyStartupWiring(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	initAt := strings.Index(text, "settings.Init(settingsDB)")
	checkAt := strings.Index(text, "validateSessionWriteSafety(dbConn != nil && dbConn.Enabled())")
	serveAt := strings.Index(text, "// ── Dynamic Timeout Config")
	if initAt < 0 || checkAt <= initAt || checkAt >= serveAt {
		t.Fatal("startup must reject missing write source after settings initialization and before request wiring")
	}
}

// TestValidSessionWriteConfigExamplesAreActuallyAccepted pins the operator
// message that the gateway prints when it refuses to start.
//
// Why this needs a gate: the message *is* the remediation for the refusal, so
// a wrong message is a defect with the same blast radius as a wrong gate.
// Two wrongnesses were live before 2026-10-20 (audit D0):
//
//   - it named `REQUEST_LOGS_WRITE_ENABLED`, an env var that does not exist
//     (the real one is derived from the key: LLM_GATEWAY_STORAGE_REQUEST_LOGS_WRITE_ENABLED);
//   - its "V2-only" line listed sessions_v2.enabled but omitted
//     sessions_v2.shadow_write, i.e. it advertised a configuration that
//     ValidateSessionWriteState **rejects**.
//
// 2026-10-29（本轮）：第三种错法是**形态错** —— 一份形态盲的列表在两种
// 部署形态里各错一半：
//
//   - PG 模式里「V1-only (no V2, no PG required)」宣传的是一个**不存在的
//     写面**（PG 的 v1 宽表已随 890 物理删除），照做之后仍然没有会话事实；
//   - Lite 模式里「PG enabled + …」是**做不到**的修法（没有 PG）。
//
// ⇒ 本门因此按**形态**逐条执行：每一行广告出来的配置，都必须在**它所属
//
//	的形态**里被校验器接受；且形态专属的不变量（PG 模式不得把 v1 当修法
//	广告出去）单独钉住。
//
// A string-equality assertion would have pinned both lies forever. This gate
// instead *executes* every advertised configuration and requires the
// validator to accept it, so the message can only contain shapes that work.
func TestValidSessionWriteConfigExamplesAreActuallyAccepted(t *testing.T) {
	previous := settings.Global
	settings.Global = settings.NewRegistry()
	settings.Init(nil)
	t.Cleanup(func() { settings.Global = previous })
	for _, spec := range append(settings.StorageSpecs(), settings.SessionsV2Specs()...) {
		settings.Global.MustRegisterSpec(spec)
	}

	assignment := regexp.MustCompile(`([A-Z][A-Z0-9_]*)=(true|false)`)
	v1Env := settings.EnvNameAuto(settings.KeyRequestLogsWriteEnabled)

	for _, shape := range []struct {
		name string
		pg   bool
	}{
		{"PG", true},
		{"Lite", false},
	} {
		t.Run(shape.name, func(t *testing.T) {
			// 校验器第一件事就是记录形态 —— 提示读的是同一份形态事实。
			_ = validateSessionWriteSafety(shape.pg)

			lines := strings.Split(validSessionWriteConfigExamples(), "\n")
			checked := 0
			for _, line := range lines {
				matches := assignment.FindAllStringSubmatch(line, -1)
				if len(matches) == 0 {
					continue
				}
				checked++
				t.Run(strings.TrimSpace(strings.SplitN(line, ":", 2)[0]), func(t *testing.T) {
					for _, m := range matches {
						t.Setenv(m[1], m[2])
					}
					// 形态由广告行自己的标记决定（[PG] / [Lite]），不是猜的。
					linePG := strings.Contains(line, "[PG]")
					require.Equal(t, shape.pg, linePG,
						"广告行的形态标记必须与本次形态一致：%s", strings.TrimSpace(line))
					if err := validateSessionWriteSafety(linePG); err != nil {
						t.Fatalf("消息里宣传的配置被启动期校验拒绝：%s\n  错误：%v", strings.TrimSpace(line), err)
					}
				})
			}
			if checked == 0 {
				t.Fatalf("%s 形态下 validSessionWriteConfigExamples() 没有解析出任何配置——门会因格式变化而恒绿", shape.name)
			}

			// ★ 形态专属不变量：PG 模式不得把 v1 开关当修法广告出去。
			//   依据：PG 的 v1 宽表已随 890 物理删除 ⇒ 该开关在 PG 侧只剩
			//   admin ingest 的正文**文件**镜像一个副作用，不是请求日志写面。
			//   广告它 = 让运维以为开它就能落库。
			if shape.pg {
				require.NotContains(t, validSessionWriteConfigExamples(), v1Env+"=true",
					"PG 模式没有 v1 写面（族表已随 890 物理删除），不得把它当修法广告出去")
			} else {
				require.NotContains(t, validSessionWriteConfigExamples(), "PG enabled",
					"Lite 模式没有 PG，「PG enabled」是做不到的修法")
				require.Contains(t, validSessionWriteConfigExamples(), v1Env+"=true",
					"Lite 模式的真修法是写 SQLite request_logs 表的 v1 开关")
			}
		})
	}
}
