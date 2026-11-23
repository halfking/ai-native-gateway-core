// e2e-bootstrap — P0-3 live E2E 专用：用 dbinit.Runner 的登记序
// fresh-install 链装出一个网关库（usage: e2e-bootstrap <dbname> <sqldir>）。
// 非安装器正式入口——是 E2E 编排对 runner 顺序知识的单一复用点。
//
// ★★ 2026-11-23 L29 轮：★ 本工具被提升为「空库新装」的**唯一烟测入口**
// （★ CI 作业直接调它 ⇒ ★ 与生产安装器**同一段 InitSchema 代码**、★ 同一份
// ★ StartupFiles、★ 同一份 embeddata ⇒ ★★ 「新装会炸」从此有一道会红的门）。
//
// ★ 容器名可用环境变量 `E2E_PG_CONTAINER` 覆盖（★ 默认仍是 `llm-gateway-pg`）：
// CI 里用默认值即可（★ CI 的容器由作业自己起、★ 名字不冲突）；
// 本机验证时用它避开**在役**的 `llm-gateway-pg` 开发库容器（★ 多项目共库、
// ★ 不可能被本工具重装）⇒ ★★ 默认值不变 ⇒ ★ 向后兼容。
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// defaultPGContainer 是历史默认值（★ 向后兼容：★ 不设环境变量时行为与从前一致）。
const defaultPGContainer = "llm-gateway-pg"

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: e2e-bootstrap <dbname> <sqldir>")
	}
	container := os.Getenv("E2E_PG_CONTAINER")
	if container == "" {
		container = defaultPGContainer
	}
	r := dbinit.NewRunner(container, "llm_gateway", os.Args[1], os.Args[2])
	if err := r.InitSchema(func(msg string) { fmt.Fprintln(os.Stderr, msg) }); err != nil {
		log.Fatal(err)
	}
	fmt.Println("bootstrap ok")
}
