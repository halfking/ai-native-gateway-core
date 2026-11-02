// e2e-bootstrap — P0-3 live E2E 专用：用 dbinit.Runner 的登记序
// fresh-install 链装出一个网关库（usage: e2e-bootstrap <dbname> <sqldir>）。
// 非安装器正式入口——是 E2E 编排对 runner 顺序知识的单一复用点。
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: e2e-bootstrap <dbname> <sqldir>")
	}
	r := dbinit.NewRunner("llm-gateway-pg", "llm_gateway", os.Args[1], os.Args[2])
	if err := r.InitSchema(func(msg string) { fmt.Fprintln(os.Stderr, msg) }); err != nil {
		log.Fatal(err)
	}
	fmt.Println("bootstrap ok")
}
