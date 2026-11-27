package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/casdoor/casdoor-go-sdk/casdoorsdk"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin/casdoor"
)

// step4: 用真 DB 池跑完整 Authenticate（含 MapOrCreate + 物化）——定位 DB 步骤失败
func runStep4() {
	cert, _ := os.ReadFile("runtime/jwt_public.pem")

	cfg := &authplugin.CasdoorConfig{
		Endpoint:     "http://127.0.0.1:18000",
		ClientID:     "e2e-client-id",
		ClientSecret: read("runtime/casdoor_client_secret"),
		Organization: "kaixuan-e2e",
		Application:  "llm-gateway-e2e",
		Certificate:  string(cert),
	}
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
		Username: "gwuser", Password: read("runtime/casdoor_user_password"),
	})
	if authErr != nil {
		fmt.Println("step4 Authenticate FAIL:", authErr)
		return
	}
	fmt.Println("step4 Authenticate OK, UserID:", result.UserID, "Success:", result.Success, "role:", result.Role)
	_ = casdoorsdk.NewClient // keep import used
}
