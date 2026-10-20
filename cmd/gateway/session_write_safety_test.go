package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
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
			pg := strings.Contains(line, "PG enabled")
			if err := validateSessionWriteSafety(pg); err != nil {
				t.Fatalf("消息里宣传的配置被启动期校验拒绝：%s\n  错误：%v", strings.TrimSpace(line), err)
			}
		})
	}
	if checked == 0 {
		t.Fatal("validSessionWriteConfigExamples() 没有解析出任何配置——门会因格式变化而恒绿")
	}
}
