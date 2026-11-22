package ursm_test

// 2026-11-23（211 配置优化遗留 L1）· 门：maintain（运维服务）接线必须
// 【可推导、可持久、可失败关闭】。
//
// 被测对象：scripts/deploy-seamless-docker.sh
//
// ── 为什么这道门存在 ─────────────────────────────────────────────────────
//
//	211 生产实测：maintain 服务跑在**宿主机** 127.0.0.1:8082（nginx 边缘已在
//	代理 /maintain-api/*），但**网关自身**没有 MAINTAIN_SERVICE_URL ⇒
//	直连网关端口的 /maintain-api/* 恒 503（maintain.not_configured），
//	启动日志恒一条 WARN「maintain proxy disabled」。
//
//	⇒ 接线必须落在**部署脚本**里（手工改容器会被下次部署冲掉）。
//	⇒ 而「接线」这件事有三个容易做错的地方，各对应一道门：
//
//	① 【地址必须是容器可达的】容器里的 127.0.0.1 是**容器自己** ⇒ 必须走
//	   docker 网络的网关 IP。⇒ 门：自动推导必须用网络网关，且必须**实测**
//	   /healthz 并确认回的是 ai-native-maintain（不是随便一个占用 8082 的进程）。
//	② 【必须跨部署保留】env 文件每次部署重写 ⇒ 运维手工加的 MAINTAIN_SERVICE_URL
//	   下一次部署就会**静默丢失**（AUTOLLM 区块登记过的同型故障面）。
//	   ⇒ 门：值必须被写回 env 文件。
//	③ 【必须失败关闭】指到一个打不通的地址比不指更糟（会 503 而不是回退）。
//	   ⇒ 门：注入必须**条件化**（空值不注入），且未配置时必须留痕说明「设计内降级」。
//
// ── 三道判据 ────────────────────────────────────────────────────────────
//
//	① 【推导判据】：自动推导用 docker 网络网关 + /healthz 探测 + 服务名确认；
//	   禁止把 127.0.0.1 直接写进 MAINTAIN_SERVICE_URL（容器里那是它自己）。
//	② 【持久判据】：非空时必须 printf 'export MAINTAIN_SERVICE_URL=%q\n' 写进 env 文件。
//	③ 【条件化判据】：注入必须在 if 分支里；且「未接线」分支必须留下一条说明日志。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func maintainDeployScriptPath(t *testing.T) string {
	t.Helper()
	root := repoRootDeployGuard(t)
	p := filepath.Join(root, ddDeployScript)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("找不到 %s: %v", p, err)
	}
	return p
}

// ── 判据 ①：推导判据 ────────────────────────────────────────────────────
func TestMaintainEnvDerivesContainerReachableAddress(t *testing.T) {
	src := readDeployScript(t) // 复用既有 helper（同包）

	// 必须用 docker 网络网关（容器里的 127.0.0.1 是容器自己）。
	if !strings.Contains(src, "docker network inspect \"$DD_NETWORK\"") {
		t.Error("脚本没有用 docker 网络网关推导 maintain 地址。\n" +
			"  ★ 容器里的 127.0.0.1 是**容器自己**，不是宿主 ⇒ 写 127.0.0.1 会打不通。")
	}
	if !strings.Contains(src, "{{(index .IPAM.Config 0).Gateway}}") {
		t.Error("脚本没有取 docker 网络的 Gateway 地址（缺 {{(index .IPAM.Config 0).Gateway}}）。")
	}

	// 必须实测 /healthz 并确认服务名（不能只看端口通不通）。
	if !strings.Contains(src, "/healthz") {
		t.Error("自动推导没有探测 /healthz：端口通 ≠ 是 maintain 服务。")
	}
	if !strings.Contains(src, "ai-native-maintain") {
		t.Error("自动推导没有确认服务名（ai-native-maintain）：\n" +
			"  ★ 随便一个占用该端口的进程都会被误认成运维服务 ⇒ 指错地址比不指更糟。")
	}
}

// ── 判据 ②：持久判据 ────────────────────────────────────────────────────
func TestMaintainEnvPersistedIntoEnvFile(t *testing.T) {
	src := readDeployScript(t) // 复用既有 helper（同包）

	// env 文件每次部署重写 ⇒ 值必须写回去，否则运维手工填的值会静默丢失。
	re := regexp.MustCompile(`printf 'export MAINTAIN_SERVICE_URL=%q\\n' "\$MAINTAIN_SERVICE_URL"`)
	if !re.MatchString(src) {
		t.Error("脚本没有把 MAINTAIN_SERVICE_URL 写进 env 文件。\n" +
			"  ★ env 文件每次部署重写 ⇒ 不写回去的话，运维手工填的地址下一次部署就丢了\n" +
			"    （AUTOLLM 区块登记过的同型故障面）。")
	}
}

// ── 判据 ③：条件化判据 ──────────────────────────────────────────────────
func TestMaintainEnvInjectionIsConditional(t *testing.T) {
	src := readDeployScript(t) // 复用既有 helper（同包）

	// 注入必须发生在条件分支里（空值不注入）。
	inj := `GW_ENV+=( -e "MAINTAIN_SERVICE_URL=${MAINTAIN_SERVICE_URL}" )`
	if !strings.Contains(src, inj) {
		t.Fatalf("脚本里没有 %s", inj)
	}
	idx := strings.Index(src, inj)
	head := src[:idx]
	// 最近的 if 条件必须在注入之前，且条件里判的是非空。
	condIdx := strings.LastIndex(head, `if [[ -n "${MAINTAIN_SERVICE_URL:-}" ]]; then`)
	if condIdx < 0 {
		t.Error("MAINTAIN_SERVICE_URL 的注入不在非空分支里：\n" +
			"  ★ 空值注入 = 把「未配置」和「配成空串」混为一谈；网关侧两者行为虽同，\n" +
			"    但 env 文件会被写进一条空的 export，运维看不出到底配没配。")
	}

	// 未接线分支必须留痕（说明这是设计内降级，而不是静默）。
	if !strings.Contains(src, "maintain 未接线") {
		t.Error("未配置 maintain 时没有留下说明日志：运维会以为「接线漏了」。")
	}
	if !strings.Contains(src, "maintain 已接线") {
		t.Error("已配置 maintain 时没有留下接线日志（地址/口径要可观测）。")
	}
}

// ── 判据 ④：bash 语法必须仍然成立 ───────────────────────────────────────
//
// 这条不是形式主义：本轮在同一个脚本里插入了函数与注入块，
// 语法错会让**整台机**的部署直接崩（脚本是唯一入口）。
func TestDeployScriptSyntaxStillValid(t *testing.T) {
	cmd := exec.Command("bash", "-n", maintainDeployScriptPath(t))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 失败：%v\n%s", err, out)
	}
}
