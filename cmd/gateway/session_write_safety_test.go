package main

import (
	"fmt"
	"os"
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
