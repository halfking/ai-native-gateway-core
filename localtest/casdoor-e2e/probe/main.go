// 探针：逐步复现 CasdoorAuthProvider.Authenticate 的调用链，定位失败步骤。
// 只读探测，不写任何状态。
package main

import (
	"fmt"
	"os"

	"github.com/casdoor/casdoor-go-sdk/casdoorsdk"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "step6" {
		runStep6()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "step5" {
		runStep5()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "step4" {
		runStep4()
		return
	}
	cert, err := os.ReadFile("runtime/jwt_public.pem")
	if err != nil {
		panic(err)
	}
	secret := read("runtime/casdoor_client_secret")
	pw := read("runtime/casdoor_user_password")

	c := casdoorsdk.NewClientWithConf(&casdoorsdk.AuthConfig{
		Endpoint:         "http://127.0.0.1:18000",
		ClientId:         "e2e-client-id",
		ClientSecret:     secret,
		Certificate:      string(cert),
		OrganizationName: "kaixuan-e2e",
		ApplicationName:  "llm-gateway-e2e",
	})

	fmt.Println("step1: GetOAuthTokenByPassword...")
	token, err := c.GetOAuthTokenByPassword("gwuser", pw)
	if err != nil {
		fmt.Println("step1 FAIL:", err)
		return
	}
	fmt.Println("step1 OK, access_token len:", len(token.AccessToken))

	fmt.Println("step2: ParseJwtToken...")
	claims, err := c.ParseJwtToken(token.AccessToken)
	if err != nil {
		fmt.Println("step2 FAIL:", err)
		return
	}
	fmt.Println("step2 OK, claims.Name:", claims.Name, "owner:", claims.Owner)

	fmt.Println("step3: GetUser...")
	u, err := c.GetUser(claims.Name)
	if err != nil {
		fmt.Println("step3 FAIL:", err)
		return
	}
	if u == nil {
		fmt.Println("step3 FAIL: user nil")
		return
	}
	fmt.Println("step3 OK, id:", u.Id, "forbidden:", u.IsForbidden)
	fmt.Println("ALL STEPS OK")
}

func read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		panic(err)
	}
	// 去尾部换行（与 hex 文件一致）
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return string(b)
}
