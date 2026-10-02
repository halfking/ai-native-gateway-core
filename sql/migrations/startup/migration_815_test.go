package startup

// migration_815_test.go — 815（canonical 视图补 origin_stage / token_band /
// client_forwarded_for）的静态契约门，2026-10-02。
//
// 门覆盖四件事，其中三件是「读起来像文档」但**必须由机器守**的：
//
//  1. 双树同步：sql/migrations/startup 与 installer embeddata 两份必须逐字节
//     相同（745 立此范式）。不同步的后果是安装器装出另一个契约的视图，而
//     本地/CI 全绿——**分歧只出现在装完的那台机器上**。
//  2. 投影里必须有源：session 分支 `t.<col>`、v1 分支 lateral `h./p.<col>`。
//     少任何一处都不是报错，而是某一臂恒 NULL（815 要修的
//     compression_stats token_band 就是这个形状）。
//  3. trace_events / id **不得**出现在投影里。这两条是本轮最贵的两个「不作为」
//     决定（trace_events 投影即净数据损失；id 证明不是同一个东西），而
//     「不作为」没有任何编译期或运行期信号——只能由门钉住，否则半年后有人
//     「顺手补齐」它们，两个洞同时打开。
//  4. down 必须能还原 740 的 115 列形态，且不得误删物理源列。

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const migration815 = "815_request_logs_view_stage_band_cff.sql"

func readMigration815(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(migration815)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", migration815, err)
	}
	return string(raw)
}

func TestMigration815InstallerEmbeddataIsByteIdentical(t *testing.T) {
	embed, err := os.ReadFile(filepath.Join("..", "..", "..",
		"installer", "cmd", "llm-gw-installer", "embeddata", "startup", migration815))
	if err != nil {
		t.Fatalf("读 installer embeddata 副本失败：%v", err)
	}
	if !bytes.Equal(embed, []byte(readMigration815(t))) {
		t.Errorf("installer embeddata/startup/%s 与 sql/migrations/startup 下的正本不一致 —— "+
			"五点同步断了。后果是安装器装出另一个列契约的视图，而本地与 CI 全绿："+
			"分歧只在装完的那台机器上出现。", migration815)
	}
	down := strings.TrimSuffix(migration815, ".sql") + ".down.sql"
	embedDown, err := os.ReadFile(filepath.Join("..", "..", "..",
		"installer", "cmd", "llm-gw-installer", "embeddata", "startup", down))
	if err != nil {
		t.Fatalf("读 installer embeddata down 副本失败：%v", err)
	}
	localDown, err := os.ReadFile(down)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", down, err)
	}
	if !bytes.Equal(embedDown, localDown) {
		t.Errorf("installer embeddata 的 down 副本与正本不一致：%s", down)
	}
}

func TestMigration815ProjectsThreeColumnsFromBothBranches(t *testing.T) {
	src := readMigration815(t)
	proj := between(t, src, "proj := $proj$", "$proj$;")
	names := between(t, src, "names := $names$", "$names$;")

	// 契约列数：740 的 115 + 3。
	if got := len(strings.Split(names, ",")); got != 118 {
		t.Errorf("names 块有 %d 列，want 118（740 的 115 + 815 的三列）", got)
	}
	for _, col := range []string{"origin_stage", "token_band", "client_forwarded_for"} {
		if !strings.Contains(proj, "t."+col+" AS "+col) {
			t.Errorf("会话分支投影缺 t.%s —— session 分臂会拿到 NULL", col)
		}
		// v1 分支：内层 lateral 追加 + 两臂选列，三处缺一就是某一臂恒 NULL。
		if !strings.Contains(src, "source."+col) {
			t.Errorf("v1 分支内层缺 source.%s（lateral 追加）", col)
		}
		if !strings.Contains(src, "h."+col) {
			t.Errorf("lateral 的 hot 臂缺 h.%s", col)
		}
		if !strings.Contains(src, "p."+col) {
			t.Errorf("lateral 的 parent 臂缺 p.%s", col)
		}
	}
	// 顺序是契约的一部分：names 末尾三列的相对次序固定，UNION ALL 按位置匹型。
	tail := names[strings.LastIndex(names, "client_ip,")+len("client_ip,"):]
	if got := strings.TrimSpace(tail); got != "origin_stage, token_band, client_forwarded_for" {
		t.Errorf("names 末尾三列次序为 %q，want \"origin_stage, token_band, client_forwarded_for\"", got)
	}
}

func TestMigration815ExcludesTraceEventsAndID(t *testing.T) {
	src := readMigration815(t)
	proj := between(t, src, "proj := $proj$", "$proj$;")
	names := between(t, src, "names := $names$", "$names$;")

	// trace_events：在 session_turns 上有同名列，但镜像从不写它（近 1/7/30 天
	// 非空率恒 0），而 v1 侧 691,883 行带值且已被反连接丢弃 ⇒ 投影它等于把
	// 691,883 行真实值换成 NULL。这是 §9.18「修好了但变全盲」的同一形状。
	for _, banned := range []string{"trace_events"} {
		if strings.Contains(proj, banned) || strings.Contains(names, banned) {
			t.Errorf("815 投影了 %s。它在 session_turns 上有列但镜像从不写（非空率恒 0），"+
				"而 v1 侧 691,883 行带值且已被反连接丢弃 —— 投影即净数据损失。"+
				"正解是先让镜像写该列（审计 §9.27 遗留项），不是把它塞进契约。", banned)
		}
	}
	// id：v1 是请求行 id、session 侧是 turn id，真库 1,515,984 组同 request_id
	// 配对里 r.id = t.id 命中 0 次。投影它等于给读方一个语义已变的同名列 ——
	// 比 NULL 更坏，因为 NULL 至少看得见。
	if regexp.MustCompile(`\bt\.id AS id\b`).MatchString(proj) ||
		regexp.MustCompile(`\bAS id\b`).MatchString(proj) && !strings.Contains(proj, "NULL::bigint AS id") {
		t.Errorf("815 投影了 id。判据是「session 侧的列与 v1 侧的是不是同一个东西」，" +
			"不是「session 侧有没有这个列名」：真库 1,515,984 组同 request_id 配对里" +
			"r.id = t.id 命中 0 次（请求行 id ≠ turn id）。它必须保持 NULL::bigint 补位。")
	}
	if !strings.Contains(proj, "NULL::bigint AS id") {
		t.Errorf("id 的 NULL 补位形态被改掉了（期望 `NULL::bigint AS id`）")
	}
}

func TestMigration815DownStripsItsOwnThreeColumns(t *testing.T) {
	raw, err := os.ReadFile(strings.TrimSuffix(migration815, ".sql") + ".down.sql")
	if err != nil {
		t.Fatalf("读 815 down 失败：%v", err)
	}
	down := string(raw)

	// 剥离锚点方向：会话分支与 v1 外层是「前导逗号 + 列名」，lateral 两臂是
	// 「列名 + 尾逗号」。用错方向会剥出列数相等但错列的 SQL。
	for _, want := range []string{
		`,[[:space:]]*t\.origin_stage`,
		`,[[:space:]]*t\.token_band`,
		`,[[:space:]]*t\.client_forwarded_for`,
		`,[[:space:]]*rl\.origin_stage`,
		`,[[:space:]]*source\.origin_stage`,
		`h\.origin_stage,[[:space:]]*`,
		`p\.origin_stage,[[:space:]]*`,
	} {
		if !strings.Contains(down, want) {
			t.Errorf("815 down 缺剥离模式 %q", want)
		}
	}
	// 半剥离检查：剥不干净就重建出一个「列数可能相等、查询仍 200、只是某一臂
	// 恒 NULL」的视图 —— 最坏结果。up 里带 EXCEPTION，这里钉住它还在。
	if !strings.Contains(down, "half-stripped view") {
		t.Error("815 down 丢了「半剥离视图」的前置拒绝 —— 剥不干净时必须拒绝重建，" +
			"而不是交付一个看起来正常的残缺视图")
	}
	// 还原目标：740 的 115 列。
	if !strings.Contains(down, "cnt <> 115") {
		t.Error("815 down 的列数对账不再指向 115（740 形态）")
	}
}

func between(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	j := strings.Index(src, end)
	if i < 0 || j < 0 || j <= i {
		t.Fatalf("815 迁移里找不到 %s … %s 块；迁移被重写时请同步本门", start, end)
	}
	return src[i+len(start) : j]
}

// TestMigration815GatesAreNotVacuous 把「这些门不是空转」做成结构性核验。
//
// 为什么需要：上面四道门里有三道是靠「迁移文件里**没有**某段文本」或
// 「**不**包含某列」来判的。这类否定式判据在一个空文件 / 被清空的登记表上
// 全部平凡通过——绿灯是真的绿，但什么都没测。变异验证能证明「改坏会红」，
// 却不证明「输入为空时也会红」。后者必须由结构断言承担，且这些断言
// **不需要重跑任何变异就能复核**。
func TestMigration815GatesAreNotVacuous(t *testing.T) {
	src := readMigration815(t)
	proj := between(t, src, "proj := $proj$", "$proj$;")
	names := between(t, src, "names := $names$", "$names$;")

	// 1) 投影与列序必须非空、且规模与 815 的契约一致（118）。若 proj 被清空，
	//    上面三道门会因为「找不到 t.<col>」而红——但那读起来像「迁移写错了」，
	//    不像「门在空输入上通过」。所以把规模钉死在这里。
	if strings.TrimSpace(proj) == "" {
		t.Fatal("815 的 $proj$ 块是空的：三道门会在空投影上平凡通过")
	}
	if n := len(strings.Split(names, ",")); n != 118 {
		t.Fatalf("815 的 names 块 %d 列，want 118（740 的 115 + 三列）。"+
			"若这是有意改动，请同步 db.canonicalColumnOrderV2 与 "+
			"TestViewV2ProjectionContractSync 的登记表", n)
	}
	// 2) 否决项必须真的「不在」文件里——而不是因为解析失败被跳过。
	//    上面那道门用的是 strings.Contains，文件被换成空串时它会通过；
	//    这里要求文件本身有可解析的内容。
	if len(src) < 2000 {
		t.Fatalf("815 迁移只有 %d 字节，疑似被截断/清空：所有基于 Contains 的门都会平凡通过", len(src))
	}
	if !strings.Contains(proj, "NULL::bigint AS id") {
		t.Error("815 的投影里没有 `NULL::bigint AS id` —— " +
			"「id 保持 NULL 补位」这条否决被改掉了（无论改成什么，" +
			"都必须先有 §9.28.3 的实测依据）")
	}
	// 3) down 必须带「半剥离拒绝」的前置断言，否则剥不干净时会交付一个
	//    「列数可能相等、查询仍 200、只是某一臂恒 NULL」的视图。
	down, err := os.ReadFile(strings.TrimSuffix(migration815, ".sql") + ".down.sql")
	if err != nil {
		t.Fatalf("读 815 down 失败：%v", err)
	}
	if len(down) < 1000 {
		t.Fatalf("815 down 只有 %d 字节，疑似被截断：它的列数对账与半剥离拒绝都会失效", len(down))
	}
	if !strings.Contains(string(down), "half-stripped view") {
		t.Error("815 down 缺「半剥离视图」的前置拒绝")
	}
	// 4) 双树副本必须都在（缺一份 = 安装器装出另一个契约的视图）。
	for _, p := range []string{
		filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer",
			"embeddata", "startup", migration815),
		filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer",
			"embeddata", "startup", strings.TrimSuffix(migration815, ".sql")+".down.sql"),
	} {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Errorf("installer 副本缺失或为空：%s（%v）", p, err)
		}
	}
}
