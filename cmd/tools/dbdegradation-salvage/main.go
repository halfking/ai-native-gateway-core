// Command dbdegradation-salvage 扫描网关侧的**降级兜底文件**，用**仓内自己的**
// 打捞通道读出记录，并输出【不含正文内容】的元数据清单。
//
// # 它要回答的问题
//
// 网关（154 / 245）在 DB 降级时会把「写库失败」的 telemetry 记录落到
// {base}/backups/sessions-YYYY-MM-DD[-NN].jsonl.gz（domains/dbdegradation 的
// FileWriter）。这些文件有两个性质同时成立：
//
//  1. ★ 它们**确实装正文**（`request_log` 记录的 payload 带 `request_body`）；
//     ⇒ 在「某段历史正文找不回来了」的问题上，它们是**唯一还活着的载体**
//     （§143.70 实测：库内为零、PG 备份为零）。
//  2. ★ 它们**大面积读不出来** —— 多进程 O_APPEND 追加 + 容器硬杀跳过
//     `gzip.Close`，会把日文件打成「孤儿 header + 完整 member + 截断尾」的混合体
//     （见 domains/dbdegradation/file_reader_salvage.go 的头注）。
//     ⇒ 单遍 `gzip.NewReader` 会在**第一个坏 member** 即整体失败 ⇒ **整文件 0 条可恢复**。
//
// ⇒ ★ 所以「这些文件还能捞回多少」这个问题，**必须用仓内的打捞通道来问**，
//
//	不能用 `zcat`。
//
// ★★ **它还回答一个更关键的问题**：捞回来的 `request_body` 是不是**累积体**
//
//	（matryoshka）？—— ★ **是**，累积体里含前几轮的全部消息 ⇒ ⇒ 一个会话只要
//	捞回**最后一轮**，其**整条正文链**都可重建 ⇒ ⇒ 「能恢复多少 turn」远大于
//	「兜底文件里出现过的 turn 行数」（§143.71 实测：50,691 vs 8,725，差 5.8×）。
//	⇒ 判据见 cmd/tools/backfill_session_bodies/derive.go 的 DeriveTurnDeltas 契约。
//
// # 这个工具做什么
//
//   - 逐文件调用 `FileReader.ReadRecordsWithStats`（= 仓内打捞路径），
//     汇总 Members / PartialMembers / SkippedLines / LostBytes 与记录条数；
//   - 为每条记录导出**元数据**（request_id / gw_session_id / event_at /
//     各正文字段的字节数与 sha256）⇒ ★ **可与库里的 session_turns 对撞，
//     但不搬运任何用户内容**；
//   - stdout 输出 JSON 汇总。
//
// ★★ **本工具只读**：它从不写备份目录；唯一的写出是可选的 -meta-out / -payload-out，
//
//	且**显式拒绝**把输出写进 {base}/backups（★ 兜底文件是唯一还活着的正文载体）。
//
// # 盲区与边界（★ 引用它的读数时必须一起说）
//
//   - ★ **LostBytes 不等于「内容没了」**：打捞是「逐 member 重同步」，member 之间的
//     垃圾字节与截断区被整体计为 LostBytes ⇒ LostBytes 是**不可恢复量的上界**。
//   - ★ **PartialMembers 的内容已被尽力解码但无法验证完整性**（硬杀跳过 gzip.Close
//     ⇒ deflate 尾块不完整、trailer 缺失）⇒ 计入记录数，但要带着这个不确定性读。
//   - ★ **只扫得到文件名匹配 `sessions-*.jsonl.gz` 的文件** ⇒ 目录里其它形态不管。
//   - ★ **元数据里的 sha256 是【正文在该文件里的字节】的哈希**，与库里正文是否逐字
//     相同是另一件事（压缩/解压、JSON 规范化都可能影响可比性）⇒ ★ 别拿它当「逐字相同」
//     的证据。
//   - ★ **本工具不判断「正文能不能重放进 session_bodies」** —— 那是另一件事
//     （见 §143.71 的诚实边界：v2 的 Message 形状与退役工具的 Msg 投影不等价）。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/dbdegradation"
)

// metaRec 是每条记录导出的元数据（★ 不含任何正文字面值）。
type metaRec struct {
	File       string `json:"file"`
	Type       string `json:"type"`
	RecordKey  string `json:"record_key,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	IDField    string `json:"id_field,omitempty"`
	SessionID  string `json:"gw_session_id,omitempty"`
	EventAt    string `json:"event_at,omitempty"`
	ReqBodyLen int    `json:"request_body_bytes"`
	ReqBodySHA string `json:"request_body_sha256,omitempty"`
	RespLen    int    `json:"response_body_bytes"`
	OutbLen    int    `json:"outbound_body_bytes"`
	// ★★ 累积深度：request_body 里 user 消息的条数 = 该条记录所累积的轮数下界。
	//   依据 §143.71 的差分语义（仓内 `DeriveTurnDeltas` = 前缀差分）：
	//   一条累积到第 K 轮的请求体含 K 条 user 消息 ⇒ K = 该会话可拆出的轮数上限。
	//   ★ 只出计数，不出任何内容。
	UserMsgs int `json:"user_msgs"`
	// TotalMsgs 是消息总条数（user + assistant + tool + system）。
	TotalMsgs int `json:"total_msgs"`
	// MsgsField 标注消息数组来自哪个字段（messages / contents / array / ""），
	// ★ 不认识的形态必须显式留空，不得静默当成 0 条。
	MsgsField string `json:"msgs_field,omitempty"`
}

// fileStat 是每个文件的汇总。
type fileStat struct {
	File           string `json:"file"`
	Bytes          int64  `json:"bytes"`
	Members        int    `json:"members"`
	PartialMembers int    `json:"partial_members"`
	SkippedLines   int    `json:"skipped_lines"`
	LostBytes      int64  `json:"lost_bytes"`
	Records        int    `json:"records"`
	RequestLog     int    `json:"request_log"`
	RequestWAL     int    `json:"request_wal"`
	ReqBodyRecords int    `json:"records_with_request_body"`
	ReqBodyBytes   int64  `json:"request_body_bytes"`
	HasLoss        bool   `json:"has_loss"`
	// ★ AccumRecords = user_msgs >= 2 的记录数（多轮累积体）。
	// ★ AccumMaxUser = 单条记录里最多的 user 消息数（= 最深的累积轮数）。
	AccumRecords int `json:"accum_records"`
	AccumMaxUser int `json:"accum_max_user_msgs"`
}

// summary 是整个扫描的汇总。
type summary struct {
	Base      string     `json:"base"`
	Prefix    string     `json:"prefix"`
	Files     []fileStat `json:"files"`
	Totals    fileStat   `json:"totals"`
	WallClock string     `json:"wall_clock"`
}

// bodyStats 取正文字段的字节数与 sha256（★ 绝不返回值本身）。
func bodyStats(v any) (int, string) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return 0, ""
		}
		sum := sha256.Sum256([]byte(t))
		return len(t), hex.EncodeToString(sum[:])
	case nil:
		return 0, ""
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return 0, ""
		}
		sum := sha256.Sum256(raw)
		return len(raw), hex.EncodeToString(sum[:])
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// bodyMsgCounts 统计请求体里的消息条数与 user 消息条数（★ 只出计数，不出内容）。
//
// 口径与仓内 `cmd/tools/backfill_session_bodies/derive.go` 的 `ParseRequestMessages` 对齐：
// 请求体可能是裸 JSON 数组，也可能是带 "messages" 字段的对象。
// ★ 额外兼容 Gemini 的 "contents" 字段，并用 msgs_field 标注来源 ⇒
//
//	不认识的形态必须显式留空（field=""），不得静默当成 0 条（否则会把
//	「没统计出来」误读成「这条记录不是累积体」）。
func bodyMsgCounts(v any) (user, total int, field string) {
	msgs, field := extractMsgs(v)
	if msgs == nil {
		return 0, 0, field
	}
	for _, m := range msgs {
		total++
		if roleOf(m) == "user" {
			user++
		}
	}
	return user, total, field
}

// extractMsgs 从请求体里取出消息数组。
func extractMsgs(v any) ([]any, string) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil, ""
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal([]byte(t), &env); err == nil {
			if raw, ok := env["messages"]; ok {
				var a []any
				if json.Unmarshal(raw, &a) == nil {
					return a, "messages"
				}
			}
			if raw, ok := env["contents"]; ok {
				var a []any
				if json.Unmarshal(raw, &a) == nil {
					return a, "contents"
				}
			}
		}
		var a []any
		if json.Unmarshal([]byte(t), &a) == nil {
			return a, "array"
		}
		return nil, ""
	case []any:
		return t, "array"
	case map[string]any:
		if a, ok := t["messages"].([]any); ok {
			return a, "messages"
		}
		if a, ok := t["contents"].([]any); ok {
			return a, "contents"
		}
		return nil, ""
	}
	return nil, ""
}

// roleOf 取一条消息的 role（★ 不碰 content）。
func roleOf(m any) string {
	switch t := m.(type) {
	case map[string]any:
		return asString(t["role"])
	case string:
		return t
	}
	return ""
}

// refuseBackupDirWrite 拒绝把输出写进 {base}/backups。
//
// ★ 为什么必须有这道护栏：兜底文件是**目前唯一还活着的历史正文载体**
//
//	（§143.70：库里没有、PG 备份没有 ⇒ 恢复面全靠它）⇒ ⇒ 一个「顺手把输出写进
//	同一个目录」的工具，可能**覆盖掉**它要读的东西 ⇒ ⇒ 这里显式拒绝。
func refuseBackupDirWrite(base, out string) error {
	if out == "" {
		return nil
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return fmt.Errorf("abs base: %w", err)
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("abs out: %w", err)
	}
	backupDir := filepath.Join(absBase, "backups")
	if absOut == backupDir || strings.HasPrefix(absOut, backupDir+string(filepath.Separator)) {
		return fmt.Errorf("refuse to write into %q：兜底文件是唯一还活着的历史正文载体（§143.70），本工具只读", backupDir)
	}
	return nil
}

// runScan 执行一次扫描（★ 可测入口：main 与单测共用）。
func runScan(ctx context.Context, base, prefix, metaOut, payloadOut string) (summary, error) {
	return runScanAnalyze(ctx, base, prefix, metaOut, payloadOut, nil, "")
}

// runScanAnalyze 执行一次扫描（★ acc 非 nil 时同时累积前缀性质分析）。
//
// ★★ 为什么要合并成【一次遍历】：兜底文件是 3~6 GB 级、逐个 gzip member 打捞 ⇒
//
//	多扫一遍就是多付 40 分钟 ⇒ ⇒ 而分析与元数据用的是同一批记录。
func runScanAnalyze(ctx context.Context, base, prefix, metaOut, payloadOut string, acc *analyzeAccum, sessionFilter string) (summary, error) {
	if err := refuseBackupDirWrite(base, metaOut); err != nil {
		return summary{}, err
	}
	if err := refuseBackupDirWrite(base, payloadOut); err != nil {
		return summary{}, err
	}

	fr := dbdegradation.NewFileReader(base)
	files, err := fr.ListBackupFiles(ctx)
	if err != nil {
		return summary{}, fmt.Errorf("列出备份文件失败：%w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })

	var metaFile, payloadFile *os.File
	if metaOut != "" {
		if metaFile, err = os.Create(metaOut); err != nil {
			return summary{}, fmt.Errorf("创建 -meta-out 失败：%w", err)
		}
		defer metaFile.Close()
	}
	if payloadOut != "" {
		if payloadFile, err = os.Create(payloadOut); err != nil {
			return summary{}, fmt.Errorf("创建 -payload-out 失败：%w", err)
		}
		defer payloadFile.Close()
	}

	sum := summary{Base: base, Prefix: prefix, Files: []fileStat{}}
	started := time.Now()
	for _, f := range files {
		if prefix != "" && !strings.HasPrefix(f.Date, prefix) {
			continue
		}
		fs := fileStat{File: f.Filename, Bytes: f.Size}
		stats, rerr := fr.ReadRecordsWithStats(ctx, f.Filename, func(rec dbdegradation.BackupRecord) error {
			var payload map[string]any
			if len(rec.Payload) > 0 {
				_ = json.Unmarshal(rec.Payload, &payload)
			}
			// ★ 会话过滤（★ 只导一个会话 ⇒ payload 体积可控 ⇒ 便于做保真度对比）。
			// ★★ 过滤【必须先于计数】⇒ 否则汇总会把「被过滤掉的记录」也算进去
			//   ⇒ ⇒ 读数与导出的元数据对不上 ⇒ ★ 口径失配。
			if sessionFilter != "" && asString(payload["gw_session_id"]) != sessionFilter {
				return nil
			}
			fs.Records++
			switch rec.Type {
			case "request_log":
				fs.RequestLog++
			case "request_wal":
				fs.RequestWAL++
			}
			// ★ 累积前缀性质分析（★ 与元数据同源 ⇒ 一次遍历出两样）。
			if acc != nil {
				acc.observe(payload)
			}
			rbLen, rbSHA := bodyStats(payload["request_body"])
			respLen, _ := bodyStats(payload["response_body"])
			outbLen, _ := bodyStats(payload["outbound_body"])
			// ★★ 累积深度计数：只对带正文的记录做（避免对无 body 记录做无用解析）。
			userMsgs, totalMsgs, msgsField := 0, 0, ""
			if rbLen > 0 {
				userMsgs, totalMsgs, msgsField = bodyMsgCounts(payload["request_body"])
			}
			if rbLen > 0 {
				fs.ReqBodyRecords++
				fs.ReqBodyBytes += int64(rbLen)
				if userMsgs >= 2 {
					fs.AccumRecords++
				}
				if userMsgs > fs.AccumMaxUser {
					fs.AccumMaxUser = userMsgs
				}
			}
			// ★★ 两种记录的主键字段名【不同】，两个都要取：
			//   - request_log 的 payload 用小写 `request_id`；
			//   - request_wal 的 payload 用大写 `RequestID`（实测：同一批兜底文件里
			//     两种都出现，且【request_wal 的 RequestID 正是 session_turns 里
			//     对应的 request_id】⇒ ★ 只取小写会让 id 集少掉一半以上
			//     ⇒ ⇒ 命中率被严重低估（本轮实测：34,088 vs 29,310 ⇒ 而少的那部分
			//     恰恰是命中率最高的那部分 ⇒ 命中率从 8,725 个 turn 掉到 165 个）。
			rid := asString(payload["request_id"])
			ridField := "request_id"
			if rid == "" {
				rid = asString(payload["RequestID"])
				if rid != "" {
					ridField = "RequestID"
				}
			}
			if metaFile != nil {
				m := metaRec{
					File:       f.Filename,
					Type:       rec.Type,
					RecordKey:  rec.RecordKey,
					RequestID:  rid,
					IDField:    ridField,
					SessionID:  asString(payload["gw_session_id"]),
					EventAt:    asString(payload["event_at"]),
					ReqBodyLen: rbLen,
					ReqBodySHA: rbSHA,
					RespLen:    respLen,
					OutbLen:    outbLen,
					UserMsgs:   userMsgs,
					TotalMsgs:  totalMsgs,
					MsgsField:  msgsField,
				}
				line, _ := json.Marshal(m)
				metaFile.Write(append(line, '\n'))
			}
			if payloadFile != nil {
				out := map[string]any{
					"type":       rec.Type,
					"record_key": rec.RecordKey,
					"timestamp":  rec.Timestamp.UTC().Format(time.RFC3339Nano),
					"payload":    json.RawMessage(rec.Payload),
				}
				line, _ := json.Marshal(out)
				payloadFile.Write(append(line, '\n'))
			}
			return nil
		})
		if rerr != nil {
			fs.HasLoss = true
			fmt.Fprintf(os.Stderr, "读 %s 出错：%v\n", f.Filename, rerr)
		}
		fs.Members = stats.Members
		fs.PartialMembers = stats.PartialMembers
		fs.SkippedLines = stats.SkippedLines
		fs.LostBytes = stats.LostBytes
		fs.HasLoss = stats.HasLoss()
		sum.Files = append(sum.Files, fs)

		t := sum.Totals
		t.File = "TOTAL"
		t.Bytes += fs.Bytes
		t.Members += fs.Members
		t.PartialMembers += fs.PartialMembers
		t.SkippedLines += fs.SkippedLines
		t.LostBytes += fs.LostBytes
		t.Records += fs.Records
		t.RequestLog += fs.RequestLog
		t.RequestWAL += fs.RequestWAL
		t.ReqBodyRecords += fs.ReqBodyRecords
		t.ReqBodyBytes += fs.ReqBodyBytes
		t.AccumRecords += fs.AccumRecords
		if fs.AccumMaxUser > t.AccumMaxUser {
			t.AccumMaxUser = fs.AccumMaxUser
		}
		t.HasLoss = t.HasLoss || fs.HasLoss
		sum.Totals = t
	}
	sum.WallClock = time.Since(started).Round(time.Millisecond).String()
	return sum, nil
}

func main() {
	base := flag.String("base", "/opt/llm-gateway-go/data",
		"兜底文件的父目录（工具会读 {base}/backups/）")
	prefix := flag.String("prefix", "",
		"只扫文件名日期以此开头的文件（★ 空 = 全扫）")
	metaOut := flag.String("meta-out", "",
		"元数据 JSONL 输出路径（★ 不含正文内容）")
	payloadOut := flag.String("payload-out", "",
		"★ 原始 payload JSONL 输出路径（含用户正文内容 ⇒ 需授权，且会占用磁盘）")
	analyze := flag.Bool("analyze-prefix", false,
		"★ 只做「累积体前缀性质」分析并输出 JSON（不写元数据、不搬正文）")
	session := flag.String("session", "",
		"★ 只处理这个 gw_session_id（★ 用于只导一个会话做保真度对比 ⇒ payload 体积可控）")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	if *analyze {
		res, err := analyzePrefix(ctx, *base, *prefix, *metaOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "分析失败：%v\n", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(os.Stderr, "输出分析结果失败：%v\n", err)
			os.Exit(1)
		}
		return
	}

	sum, err := runScanAnalyze(ctx, *base, *prefix, *metaOut, *payloadOut, nil, *session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "扫描失败：%v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sum); err != nil {
		fmt.Fprintf(os.Stderr, "输出汇总失败：%v\n", err)
		os.Exit(1)
	}
	if *metaOut != "" {
		fmt.Fprintf(os.Stderr, "元数据已写入 %s\n", filepath.Clean(*metaOut))
	}
	if *payloadOut != "" {
		fmt.Fprintf(os.Stderr, "payload 已写入 %s（★ 含用户正文内容）\n", filepath.Clean(*payloadOut))
	}
}
