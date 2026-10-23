//go:build !integration

package main

// §143.96 的门：★ 本轮是【会话族 final_success 自愈（copy-gap 修复）落仓轮】
//
// ★★★★★★ 这个门要守的是【五件不许被无声改掉的事】：
//
//	① ★★★★★★ 【897 的函数体必须逐字等于「869 + 一处 demote 插入」】
//	   —— ★ 这是「只落仓一个自愈步、其余零语义变更」这条承诺的**唯一可执行证据**
//	   —— ★ 一旦有人顺手改了 869 的两道牙（列清单 / 形状守卫），这里立刻红。
//	② ★★★★★★ 【demote 步必须在【批次选取之前】】
//	   —— ★ 位置本身就是语义：★ 放在批次之后 = 降级的是已经被搬走的行 = 一点用没有。
//	③ ★★★★★★ 【demote 的键必须是【父表部分唯一索引的键】】
//	   —— ★ (tenant_id, session_id, partition_date) WHERE is_final_success
//	   —— ★ 用错键（promote 自己的 (tenant_id, request_id)）= 假阴性 = 白写。
//	④ ★★★★★★ 【六点登记链不许断】
//	   —— ★ canonical / embeddata / go:embed + map / StartupFiles / tsv /
//	   —— ★ 通道腿 files=() + intentional_function_chains。
//	⑤ ★★★★ 【回滚体必须逐字等于 869】—— ★ 因为「回滚 = 回到故障」这句话
//	   只有在 down 体真的把 demote 撤掉时才成立。
//
// ★ ★★ 纪律：★ 本门是【登记门 + 形态门】，★ 不是端到端门。
// ★ ★★ 判据一律查【可执行面】（★ 剥掉 `--` 注释）⇒ ⇒ 在注释里写出那行不会假绿。
// ★ ★★ token 搜索一律用 §143.95 轮落地的【带边界】实现（★ 同包共用一份实现
// ★ ★★ ⇒ 避免两轮判据漂移）⇒ ⇒ 「改名成后缀版」逃不掉。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14396Migration = "sql/migrations/startup/897_session_turns_promote_final_success_self_heal.sql"
	section14396Down      = "sql/migrations/startup/897_session_turns_promote_final_success_self_heal.down.sql"
	section14396Baseline  = "sql/migrations/startup/869_promote_session_turns_trace_events.sql"

	section14396Embed     = "installer/cmd/llm-gw-installer/embeddata/startup/897_session_turns_promote_final_success_self_heal.sql"
	section14396EmbedDown = "installer/cmd/llm-gw-installer/embeddata/startup/897_session_turns_promote_final_success_self_heal.down.sql"

	section14396InstallerMain = "installer/cmd/llm-gw-installer/main.go"
	section14396Runner        = "installer/internal/dbinit/runner.go"
	section14396Manifest      = "sql/schema/installed_startup_migrations.tsv"
	section14396Channel       = "scripts/apply-db-revision-sequence.sh"

	section14396Runbook = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14396Lessons = "LESSONS.md"
	section14396Agents  = "AGENTS.md"
)

// section14396FileName 是 897 的【纯文件名】——★ 登记链的六个点全部按它对拍。
const section14396FileName = "897_session_turns_promote_final_success_self_heal.sql"

func repoRoot14396(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14396(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14396(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// sqlExecutable14396 剥掉【整行注释】后的可执行面。
//
// ★ 与 §143.95 轮同一口径：★ 判据必须查【真正会被 PG 执行的那一面】，
// ★ 否则「在注释里写出那行」就会假绿。
func sqlExecutable14396(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14396(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// funcBody14396 截出 promote 函数的【函数体】（从 CREATE OR REPLACE FUNCTION
// 到收尾的 `$$;`）——★ 按行给出，便于逐行比对。
func funcBody14396(t *testing.T, rel string) []string {
	t.Helper()
	exe := sqlExecutable14396(t, rel)
	head := "CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition("
	start := strings.Index(exe, head)
	if start < 0 {
		t.Fatalf("★ %s 里没有 `%s` —— ★ 判据失效：★ 门读不到函数体却报绿", rel, head)
	}
	rest := exe[start:]
	end := strings.Index(rest, "\n$$;")
	if end < 0 {
		t.Fatalf("★ %s 里找不到函数体的收尾 `$$;` —— ★ 判据失效", rel)
	}
	return strings.Split(rest[:end], "\n")
}

// demoteSegment14396 定位 897 函数体里【demote 插入段】的行区间。
//
// ★ 段 = 「紧挨着 demote UPDATE 之前的那条 END IF;」+ demote 本体 +
// ★ 「v_demoted;」——★ 因为 869 的那条 END IF; 在 897 里被推到了段尾。
// ★ 返回 [start, end) 的半开区间。
func demoteSegment14396(t *testing.T, body []string) (int, int) {
	t.Helper()
	idxUpdate := -1
	idxDiag := -1
	for i, l := range body {
		if idxUpdate < 0 && l == "UPDATE public.session_turns_hot h" {
			idxUpdate = i
		}
		if idxDiag < 0 && strings.HasPrefix(l, "GET DIAGNOSTICS v_demoted = ROW_COUNT;") {
			idxDiag = i
		}
	}
	if idxUpdate < 0 {
		t.Fatalf("★ 897 的函数体里没有 demote 的 `UPDATE public.session_turns_hot h` —— ★ 判据失效")
	}
	if idxDiag < 0 {
		t.Fatalf("★ 897 的函数体里没有 `GET DIAGNOSTICS v_demoted = ROW_COUNT;` —— ★ 判据失效")
	}
	if idxUpdate == 0 {
		t.Fatalf("★ 897 的函数体里 demote 之前没有任何行 —— ★ 判据失效")
	}
	start := idxUpdate - 1
	end := idxDiag + 4 // GET DIAGNOSTICS / IF v_demoted > 0 THEN / RAISE WARNING / v_demoted;
	if end > len(body) {
		t.Fatalf("★ 897 的函数体里 demote 段越界 —— ★ 判据失效")
	}
	return start, end
}

// TestSection14396MigrationFilesExistAndEmbedIsByteIdentical —— ★ 判据④的前半
//
// ★★ 迁移必须在【六个点位】同步登记，而 embeddata 副本必须与 canonical
// ★★ 逐字节相同（★ installer 真正跑的是 embeddata 那份 ⇒ ★ 漂了就等于装了个别的）。
func TestSection14396MigrationFilesExistAndEmbedIsByteIdentical(t *testing.T) {
	canon, err := os.ReadFile(filepath.Join(repoRoot14396(t), section14396Migration))
	if err != nil {
		t.Fatalf("★ canonical 迁移件缺失 %s：%v", section14396Migration, err)
	}
	embed, err := os.ReadFile(filepath.Join(repoRoot14396(t), section14396Embed))
	if err != nil {
		t.Fatalf("★ embeddata 副本缺失 %s：%v —— ★ installer 真正执行的是 embeddata 那份",
			section14396Embed, err)
	}
	if string(canon) != string(embed) {
		t.Errorf("★ embeddata 副本与 canonical 逐字节不一致：%s vs %s —— "+
			"★ ★★ 改了一处忘了另一处 ⇒ ⇒ 安装器会带着非正典 SQL 上门（849/850/851 那一族）",
			section14396Migration, section14396Embed)
	}
	canonDown, err := os.ReadFile(filepath.Join(repoRoot14396(t), section14396Down))
	if err != nil {
		t.Fatalf("★ canonical down 件缺失 %s：%v", section14396Down, err)
	}
	embedDown, err := os.ReadFile(filepath.Join(repoRoot14396(t), section14396EmbedDown))
	if err != nil {
		t.Fatalf("★ embeddata down 副本缺失 %s：%v —— ★ down 也要五点同步",
			section14396EmbedDown, err)
	}
	if string(canonDown) != string(embedDown) {
		t.Errorf("★ embeddata down 副本与 canonical 逐字节不一致：%s vs %s",
			section14396Down, section14396EmbedDown)
	}
}

// TestSection14396BodyIsBaselinePlusExactlyOneDemote —— ★★ 判据①（★ 本门最强的一条）
//
// ★★★★★★ 【897 的函数体必须逐字等于「869 的函数体 + 一处 demote 插入」】——★★★
//
//	★ 减掉两样东西：
//	  ① DECLARE 里的 `v_demoted BIGINT := 0;`
//	  ② demote 插入段（UPDATE … GET DIAGNOSTICS … v_demoted;）
//	★ 剩下的必须与 869 的函数体【逐行逐字相同】。
//
// ★⇒⇒ 这条把「只落仓一个自愈步、其余零语义变更」从【口头承诺】变成【可执行证据】。
// ★⇒⇒ 任何人顺手改 869 的两道牙（目录派生列清单 / 形状守卫 / advisory lock /
// ★⇒⇒ 搬运 CTE），这里立刻红 ⇒ ⇒ 改动就必须被显式地重新基线化。
func TestSection14396BodyIsBaselinePlusExactlyOneDemote(t *testing.T) {
	base := funcBody14396(t, section14396Baseline)
	heal := funcBody14396(t, section14396Migration)

	start, end := demoteSegment14396(t, heal)

	var norm []string
	for i, l := range heal {
		if i >= start && i < end {
			continue
		}
		if l == "v_demoted BIGINT := 0;" {
			continue
		}
		norm = append(norm, l)
	}

	if len(norm) != len(base) {
		t.Fatalf("★ 897 减去 demote 段后有 %d 行，869 的函数体有 %d 行 —— "+
			"★ ★★★★ 897 必须是「869 + 一处 demote 插入」，★ 多出来的行说明有人动了别的地方",
			len(norm), len(base))
	}
	for i := range base {
		if norm[i] != base[i] {
			t.Errorf("★ 897 减去 demote 段后，第 %d 行与 869 的函数体不同：\n"+
				"★   897: %s\n★   869: %s\n"+
				"★ ★★★★ 897 必须是「869 + 一处 demote 插入」⇒ ⇒ 其余任何改动都要显式重新基线化",
				i+1, norm[i], base[i])
		}
	}
}

// TestSection14396DemoteRunsBeforeBatchSelection —— ★★ 判据②（★ 位置即语义）
//
// ★★★★★★ 【demote 必须发生在【批次选取】之前】——★★★
//
//	★ 降级的目的是「让这行带着 FALSE 进批次」⇒ ⇒ 降级必须在批次被选出来之前。
//	★ 放在批次之后 ⇒ ⇒ 被降级的是【已经被搬走的那一行】⇒ ⇒ 一点用没有，
//	★ 而且会静默地「看起来装上了」。
func TestSection14396DemoteRunsBeforeBatchSelection(t *testing.T) {
	exe := sqlExecutable14396(t, section14396Migration)
	demoteAt := strings.Index(exe, "UPDATE public.session_turns_hot h")
	batchAt := strings.Index(exe, "INSERT INTO session_turns_promotion_batch (")
	if demoteAt < 0 {
		t.Fatalf("★ 897 的可执行面里没有 demote 的 `UPDATE public.session_turns_hot h` —— ★ 判据失效")
	}
	if batchAt < 0 {
		t.Fatalf("★ 897 的可执行面里没有批次选取 `INSERT INTO session_turns_promotion_batch (` —— ★ 判据失效")
	}
	if demoteAt > batchAt {
		t.Errorf("★ 897 里 demote 步（偏移 %d）出现在批次选取（偏移 %d）**之后** —— "+
			"★ ★★★★ 降级的目的是「让这行带着 FALSE 进批次」"+
			"★ ⇒ ⇒ 放在后面 = 降级的是已经被搬走的行 = 自愈完全无效",
			demoteAt, batchAt)
	}
}

// TestSection14396DemoteKeyIsParentUniqueIndexKey —— ★★ 判据③（★ 最容易写错的一条）
//
// ★★★★★★ 【demote 的键必须是【父表部分唯一索引的键】】——★★★
//
//	★ 键 = (tenant_id, session_id, partition_date)，谓词 = 父表 is_final_success IS TRUE
//	★ ⚠ 反面：promote 自己的去重键是 (tenant_id, request_id)
//	★ ⚠ ★★ 用错键 ⇒ 查出来恒为 0 ⇒ ⇒ demote 一次都不触发 ⇒ ⇒ 白写。
//
// ★ 这正是 §143.95 轮自己第一次查错的地方 ⇒ ⇒ 本门把正确键逐字钉住。
func TestSection14396DemoteKeyIsParentUniqueIndexKey(t *testing.T) {
	body := funcBody14396(t, section14396Migration)
	start, end := demoteSegment14396(t, body)
	demote := strings.Join(body[start:end], "\n")

	for _, needle := range []string{
		"UPDATE public.session_turns_hot h",
		"SET is_final_success = FALSE",
		"WHERE h.is_final_success IS TRUE",
		"AND h.ts < statement_timestamp() - p_retention",
		"FROM public.session_turns p",
		"WHERE p.tenant_id = h.tenant_id",
		"AND p.session_id = h.session_id",
		"AND p.partition_date = h.partition_date",
		"AND p.is_final_success IS TRUE",
		"GET DIAGNOSTICS v_demoted = ROW_COUNT;",
	} {
		if !containsToken14395(demote, needle) {
			t.Errorf("★ 897 的 demote 段里没有逐字的 `%s` —— "+
				"★ ★★★★ demote 的键必须是父表部分唯一索引 "+
				"`uq_session_turns_final_success` 的键 "+
				"(tenant_id, session_id, partition_date) WHERE is_final_success"+
				"★ ⇒ ⇒ ★★ 少了 session_id / partition_date ⇒ ⇒ 跨 UTC 日的同会话会被漏掉",
				needle)
		}
	}

	// ★★★ 反向禁：demote 段里**不得**出现 request_id 形态的键。
	for _, bad := range []string{
		"p.request_id = h.request_id",
		"h.request_id",
		"p.is_final_success IS FALSE",
		"h.is_final_success IS FALSE",
	} {
		if containsToken14395(demote, bad) {
			t.Errorf("★ 897 的 demote 段里出现了 `%s` —— "+
				"★ ★★★★ promote 自己的去重键是 (tenant_id, request_id)⇒ ★★ 按它查冲突恒得 0 行"+
				"★ ⇒ ⇒ ★★ 那是【查错了键】★ ⇒ ⇒ demote 必须按父表唯一索引的键来判",
				bad)
		}
	}
}

// TestSection14396BaselineTeethAreKept —— ★★ 判据④（869 的两道牙）
//
// ★★★★★★ 【869 的「目录派生列清单 + 形状守卫」两道牙不许被 897 顺手改掉】——★★★
//
//	★ ① 目录派生列清单：v_cols 从 pg_attribute 派生 ⇒ ⇒ 对称 ADD COLUMN 自动流经晋级，
//	   不对称的形状漂移则响亮失败 ⇒ ⇒ 两头都不再静默丢列。
//	★ ② 形状守卫：父表 / hot 两侧的 name:type:notnull 串必须一致。
//	★ ③ ★★ 「派生清单不得按列名过滤」这道后置断言必须在位 ——
//	   ★★ 2026-10-16 变异验证实测：★ 只靠形态断言时，注入 `attname <> 'trace_events'`
//	   ★★ 的 869 仍然「安装成功」⇒ ⇒ 数据照样被丢。
func TestSection14396BaselineTeethAreKept(t *testing.T) {
	exe := sqlExecutable14396(t, section14396Migration)

	for _, needle := range []string{
		"INTO v_cols",
		"FROM pg_attribute",
		"quote_ident(attname)",
		"WHERE attrelid = 'public.session_turns_hot'::regclass",
		"INSERT INTO public.session_turns (%s)",
		"INTO v_parent_shape",
		"INTO v_hot_shape",
		"IS DISTINCT FROM",
		"RAISE EXCEPTION 'session_turns hot/parent column contract has drifted (column set mismatch)'",
	} {
		if !containsToken14395(exe, needle) {
			t.Errorf("★ 897 的可执行面里没有逐字的 `%s` —— "+
				"★ ★★★★ 897 的基线是 869 ⇒ ⇒ 869 的「目录派生列清单 + 形状守卫」两道牙必须原样在位"+
				"★ ⇒ ⇒ ★★ 少一道 ⇒ ⇒ 静默丢列的老坑（54 列硬编码清单丢掉 52 列）就回来了",
				needle)
		}
	}

	// ★ 派生清单本身不得按列名过滤 —— ★ 两处 v_cols 查询都要查。
	rest := exe
	checked := 0
	for {
		at := strings.Index(rest, "INTO v_cols")
		if at < 0 {
			break
		}
		seg := rest[at:]
		if semi := strings.Index(seg, ";"); semi >= 0 {
			seg = seg[:semi]
		}
		checked++
		if strings.Contains(seg, "attname <>") || strings.Contains(seg, "attname !=") {
			t.Errorf("★ 897 有一处 v_cols 查询被按列名过滤（`attname <>` / `attname !=`）—— "+
				"★ ★★★★ 形态检查照样通过，★ 而数据照样被丢"+
				"★ ⇒ ⇒ ★★ 派生清单必须是无过滤的目录形态（第 %d 处）",
				checked)
		}
		rest = rest[at+len("INTO v_cols"):]
	}
	if checked == 0 {
		t.Fatalf("★ 897 的可执行面里一处 `INTO v_cols` 都没找到 —— ★ 判据失效")
	}
}

// TestSection14396PostConditionAssertsDemoteInstalled —— ★★ 判据⑤
//
// ★★★★★★ 【897 必须自带「demote 步在位」的牙】——★★★
//
// ★ 没有它 ⇒ 本迁移只是把 869 重新装了一遍 ⇒ ⇒ 23505 照旧 ⇒ ⇒
// ★ 而「安装成功」会掩盖「什么也没修」⇒ ⇒ ★ 这正是 copy-gap 的第二种形态。
func TestSection14396PostConditionAssertsDemoteInstalled(t *testing.T) {
	exe := sqlExecutable14396(t, section14396Migration)
	for _, needle := range []string{
		"IF v_prosrc NOT LIKE '%SET is_final_success = FALSE%' THEN",
		"RAISE EXCEPTION '897 post-condition failed: demote step missing (the self-heal was not installed)'",
		"IF v_prosrc IS NULL THEN",
		"AND pg_get_function_identity_arguments(p.oid) = 'p_retention interval, p_batch_size integer'",
	} {
		if !containsToken14395(exe, needle) {
			t.Errorf("★ 897 的可执行面里没有逐字的 `%s` —— "+
				"★ ★★★★ 后置条件必须断言「装上去的函数体里真的有 demote 步」"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 「安装成功」会掩盖「自愈没装上」",
				needle)
		}
	}
	// ★ 后置条件里的「派生清单未被按列名过滤」那道断言也必须在位。
	guard := `INTO\s+v_cols[^;]*attname\s*(<>|!=)\s*`
	if !strings.Contains(exe, guard) {
		t.Errorf("★ 897 的可执行面里没有 `INTO\\s+v_cols[^;]*attname\\s*(<>|!=)\\s*` 这道断言 —— "+
			"★ ★★★ 2026-10-16 变异验证实测：★ 只靠形态断言时，"+
			"★ ★★ 注入 `attname <> 'trace_events'` 的迁移仍然「安装成功」⇒ ⇒ 数据照样被丢（守卫原文：%s）",
			guard)
	}
}

// TestSection14396DownScriptReplaysBaseline —— ★★ 判据⑥（★ 回滚语义）
//
// ★★★★★★ 【down 体必须逐字等于 869 的函数体】——★★★
//
// ★ 「回滚 = 回到故障」这句话只有在 down 体真的把 demote 撤掉时才成立。
// ★ ⚠ 而且：★ 897 的基线（869）**仍在生产链上**⇒ ⇒ 回滚路径应当是**可执行**的
// ★ （★ 695 写「无」是因为它的基线 688 已随 v1 族退役 ⇒ ⇒ 没人需要回滚它）。
func TestSection14396DownScriptReplaysBaseline(t *testing.T) {
	base := funcBody14396(t, section14396Baseline)
	down := funcBody14396(t, section14396Down)

	if len(down) != len(base) {
		t.Fatalf("★ down 体的函数体有 %d 行，869 的函数体有 %d 行 —— "+
			"★ ★★★★ down 必须逐字重放 869（★ 也就是「撤掉 demote 步」）",
			len(down), len(base))
	}
	for i := range base {
		if down[i] != base[i] {
			t.Errorf("★ down 体的函数体第 %d 行与 869 不同：\n"+
				"★   down: %s\n★   869 : %s\n"+
				"★ ★★★★ down ≠ 869 ⇒ ⇒ 「回滚 = 回到故障」这句话就不成立",
				i+1, down[i], base[i])
		}
	}
	if containsToken14395(strings.Join(down, "\n"), "SET is_final_success = FALSE") {
		t.Errorf("★ down 体里还有 `SET is_final_success = FALSE` —— " +
			"★ ★★★★ down 必须撤掉 demote 步⇒ ⇒ 否则「回滚」根本不是回滚")
	}
}

// TestSection14396RegistrationChainIsComplete —— ★★ 判据⑦（六点登记链）
//
// ★★★★★★ 【六个点位一个都不能少】——★★★
//
//	① canonical 文件
//	② embeddata 副本（★ 且与 canonical 逐字节相同 ⇒ 上一条已查）
//	③ installer 的 go:embed 指令 + 变量声明 + embeddedSQLFiles 映射
//	④ installer 的 StartupFiles（dbinit runner）
//	⑤ installed_startup_migrations.tsv（★ 自动生成 ⇒ 用 manifest 门对拍）
//	⑥ 通道腿 files=() + intentional_function_chains（★ 且 897 必须是链的最后一项）
//
// ★ ⚠ 第 ⑥ 项的「最后一项」不是形式：★ 链序 = 投递序 ⇒ ★ 「最后一项胜出」
// ★ ⇒ ⇒ 897 必须排在 869 之后 ⇒ ⇒ 否则 897 的函数体会被 869 覆盖 ⇒ ⇒ 自愈静默失效。
func TestSection14396RegistrationChainIsComplete(t *testing.T) {
	// ③ installer main.go
	main := readRepoFile14396(t, section14396InstallerMain)
	for _, needle := range []string{
		"//go:embed embeddata/startup/" + section14396FileName,
		"var sessionTurnsPromoteFinalSuccessSelfHeal897 []byte",
		"\"startup/" + section14396FileName + "\":",
	} {
		if !containsToken14395(main, needle) {
			t.Errorf("★ %s 里没有逐字的 `%s` —— "+
				"★ ★★★★ installer 的 embed 登记是**独立于 canonical 目录**的一条腿"+
				"★ ⇒ ⇒ ★★ 少一处 ⇒ ⇒ 全新安装拿不到 897（★ 而升级库与新装库会分叉）",
				section14396InstallerMain, needle)
		}
	}

	// ④ StartupFiles
	runner := readRepoFile14396(t, section14396Runner)
	if !containsToken14395(runner, "\""+section14396FileName+"\",") {
		t.Errorf("★ %s 的 StartupFiles 里没有 `\"%s\",` —— "+
			"★ ★★★★ StartupFiles 是新装库的投递清单（★ dbinit runner）",
			section14396Runner, section14396FileName)
	}

	// ⑤ manifest tsv
	tsv := readRepoFile14396(t, section14396Manifest)
	found := false
	for _, line := range strings.Split(tsv, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) >= 2 && strings.TrimSpace(fields[1]) == section14396FileName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("★ %s 里没有登记 `%s` —— "+
			"★ ★★★★ 该 tsv 由 `cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update` 生成"+
			"★ ⇒ ⇒ ★★ 手改它没有意义（★ 头注释写明 DO NOT EDIT BY HAND）",
			section14396Manifest, section14396FileName)
	}

	// ⑥ 通道腿
	channel := readRepoFile14396(t, section14396Channel)
	if !containsToken14395(channel, "\"$ROOT_DIR/sql/migrations/startup/"+section14396FileName+"\"") {
		t.Errorf("★ %s 的 files=() 数组里没有登记 897 —— "+
			"★ ★★★★ 通道腿是**存量库升级**的投递路径（★ 仓内唯一可自证的那条腿）"+
			"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 存量库永远拿不到 897",
			section14396Channel)
	}

	// ★ intentional_function_chains：897 必须在这个链里，且必须是**最后一项**。
	chainLine := ""
	for _, line := range strings.Split(channel, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "'promote_session_turns_hot_to_partition|") {
			chainLine = trimmed
			break
		}
	}
	if chainLine == "" {
		t.Fatalf("★ %s 里找不到 promote_session_turns_hot_to_partition 的 intentional_function_chains 行 —— ★ 判据失效",
			section14396Channel)
	}
	if !strings.Contains(chainLine, "|897_session_turns_promote_final_success_self_heal.sql|") {
		t.Errorf("★ intentional_function_chains 里没有 897 —— "+
			"★ ★★★★ 897 与 707/640/869 **重定义同一个函数**⇒ ⇒ 不登记 = 部署在应用任何文件之前 abort（exit 5）（文件：%s）",
			section14396Channel)
	}
	if !strings.HasSuffix(chainLine, "897_session_turns_promote_final_success_self_heal.sql|'") {
		t.Errorf("★ intentional_function_chains 里 897 **不是最后一项** —— "+
			"★ ★★★★ 链序 = 投递序 ⇒ ⇒「最后一项胜出」⇒ ⇒ 897 必须排在 869 之后"+
			"★ ⇒ ⇒ ★★ 否则 897 的函数体被 869 覆盖 ⇒ ⇒ 自愈静默失效（★ 而迁移自己还会报成功）（文件：%s）",
			section14396Channel)
	}
}

// TestSection14396DocsAreRegistered —— ★★ 判据⑧（★ 过程可查）
//
// ★ 落仓的东西必须能被人查到 ⇒ ⇒ 三份文档都要有登记。
func TestSection14396DocsAreRegistered(t *testing.T) {
	runbook := readRepoFile14396(t, section14396Runbook)
	if !strings.Contains(runbook, "§143.96") {
		t.Errorf("★ %s 里没有 `§143.96` 段 —— ★ 落仓过程必须可查", section14396Runbook)
	}
	lessons := readRepoFile14396(t, section14396Lessons)
	if !strings.Contains(lessons, "## §290") {
		t.Errorf("★ %s 里没有 `## §290` 段 —— ★ 通用式必须沉淀", section14396Lessons)
	}
	agents := readRepoFile14396(t, section14396Agents)
	if !strings.Contains(agents, "R140") {
		t.Errorf("★ %s 里没有 `R140` 登记 —— ★ 交接必须可查", section14396Agents)
	}
}
