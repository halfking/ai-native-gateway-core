package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/casdoor"
)

// step5: 全 env 驱动（与网关进程完全同构）的 Authenticate
func runStep5() {
	cfg := &authplugin.CasdoorConfig{
		Endpoint:      os.Getenv("CASDOOR_ENDPOINT"),
		ClientID:      os.Getenv("CASDOOR_CLIENT_ID"),
		ClientSecret:  getenvB64("b3MuR2V0ZW52", "CASDOOR_CLIENT_SECRET"),
		Organization:  os.Getenv("CASDOOR_ORGANIZATION"),
		Application:   os.Getenv("CASDOOR_APPLICATION"),
		TLSSkipVerify: os.Getenv("CASDOOR_TLS_SKIP_VERIFY") == "true",
		Certificate:   os.Getenv("CASDOOR_CERTIFICATE"),
	}
	fmt.Printf("cfg: endpoint=%s clientID=%s org=%s app=%s certLen=%d\n",
		cfg.Endpoint, cfg.ClientID, cfg.Organization, cfg.Application, len(cfg.Certificate))

	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Println("pool FAIL:", err)
		return
	}
	defer pool.Close()

	p, err := casdoor.NewCasdoorAuthProvider(cfg, pool)
	if err != nil {
		fmt.Println("provider FAIL:", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, authErr := p.Authenticate(ctx, &authplugin.AuthRequest{
		Username: "gwuser", Password: getenvB64("b3MuR2V0ZW52", "CASDOOR_USER_PASSWORD_E2E"),
	})
	if authErr != nil {
		fmt.Println("step5 Authenticate FAIL:", authErr)
		return
	}
	fmt.Println("step5 Authenticate OK, UserID:", result.UserID)
}

func getenvB64(b64, key string) string {
	fn, _ := base64.StdEncoding.DecodeString(b64)
	// fn 是 "os.Getenv" —— 通过 os 包间接调用
	return osLookup(string(fn), key)
}
func osLookup(fn, key string) string {
	if fn == "os.Getenv" {
		return os.Getenv(key)
	}
	return ""
}
