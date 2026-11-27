// 检查网关侧进程内的 oauth2 token endpoint 是否被 casdoor 拒绝：模拟 oauth2.AuthStyleInParams 提交
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func runStep6() {
	secret := mustRead6("runtime/casdoor_client_secret")
	pw := mustRead6("runtime/casdoor_user_password")
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", "gwuser")
	form.Set("password", pw)
	form.Set("client_id", "e2e-client-id")
	form.Set("client_secret", secret)
	resp, err := http.Post("http://127.0.0.1:18000/api/login/oauth/access_token",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		fmt.Println("FAIL:", err)
		return
	}
	defer resp.Body.Close()
	buf := make([]byte, 400)
	n, _ := resp.Body.Read(buf)
	fmt.Println("status:", resp.Status)
	fmt.Println("body:", string(buf[:n]))
	_ = os.Stdout
}

func mustRead6(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		panic(err)
	}
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return string(b)
}
