package admin

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// heavyMeasurementAllowed decides whether a **measurement-grade** database
// statement may be run against the host named by a DSN.
//
// # 为什么这道闸存在
//
// 审计 §8.3 的正文取数门必须执行 `EXPLAIN (ANALYZE, BUFFERS)` 才能测到真实
// 代价，而这条语句在列存分区上实测 **15.7~17.4 秒/会话**（真实生产函数
// querySessionBodiesByRequestID，200 个 request_id，本机连测三次）。
// 同一文件里的等价性门更重：legacy 单查询约 0.8 秒/轮，探针轮数上界 60。
//
// 这类语句是**分析型负载**，而 2026-10-01 的 252 SQL 日志审计（R17 报告
// §一/§二.5）刚记录了它的代价：一条同族的 19GB 级测量扫描在 4 核共享小机上
// 独占 IO 1 小时 32 分，主机 load 20~25，**把生产写入链整体饿死**——
// 05:31-07:05 写链冻结窗的根因判定就是「主机饱和下的探测饿死」。
// R17 纪律 ㊺：共享小机上，测量型长查询受负载预算约束。
//
// 也就是说：这道门自己就是那类事故的**发生器之一**。集成门凭一个环境变量
// 就能指向任意主机，而环境变量是最容易被「顺手指到 252/154」的那一个。
// 所以默认只允许回环 / 本地 socket；真要打远端，必须显式 opt-in。
//
// # 为什么放在未打 tag 的 _test.go 里
//
// 真库门带 `//go:build integration`，而这道判据要能在**不带数据库**的
// 普通单测里被覆盖（见 db_measurement_host_guard_table_test.go）。
// 同一包的 untagged 与 tagged 测试文件会一起编进测试二进制，
// 所以 untagged 的 _test.go 是唯一既不进生产二进制、又能被 tagged 门复用的位置。
const heavyMeasurementOptInEnv = "LLM_GATEWAY_ALLOW_HEAVY_DB_MEASUREMENT"

// heavyMeasurementAllowed reports whether the statement may run, plus the
// reason to surface in a skip message. It **fails closed**: an unparseable DSN
// is not treated as local.
func heavyMeasurementAllowed(dsn, optIn string) (bool, string) {
	host, err := dsnHost(dsn)
	if err != nil {
		return false, fmt.Sprintf("cannot determine the database host from %q (%v); "+
			"refusing to run a measurement-grade statement against an unknown host", redactDSN(dsn), err)
	}
	if host == "" {
		// Unix socket / keyless DSN: same machine by construction.
		return true, ""
	}
	if isLoopbackHost(host) {
		return true, ""
	}
	if truthy(optIn) {
		return true, fmt.Sprintf("%s=%s acknowledged: running a measurement-grade "+
			"statement against non-local host %q — it is an analytical workload and "+
			"has starved a production write path before (252 audit round 17)",
			heavyMeasurementOptInEnv, optIn, host)
	}
	return false, fmt.Sprintf("target host %q is not loopback and this statement is an "+
		"analytical workload (measured 15.7~17.4 s on a columnar partition; an "+
		"equivalent scan starved a 4-core shared host's write path for 1h32m in the "+
		"252 audit round 17). Set %s=1 to run it anyway.", host, heavyMeasurementOptInEnv)
}

func dsnHost(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	// postgres://user:pw@host:port/db -> u.Host
	if u.Host == "" {
		// key=value DSN form: host=...
		for _, kv := range strings.Fields(dsn) {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "host") {
				return v, nil
			}
		}
		return "", nil
	}
	if h, _, err := net.SplitHostPort(u.Host); err == nil {
		return h, nil
	}
	return u.Host, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// redactDSN keeps the failure message printable without leaking a password.
func redactDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		u.User = url.User(u.User.Username())
		return u.String()
	}
	if i := strings.Index(dsn, "password="); i >= 0 {
		return dsn[:i] + "password=***"
	}
	return dsn
}
