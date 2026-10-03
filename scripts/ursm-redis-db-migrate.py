#!/usr/bin/env python3
# =====================================================================
# scripts/ursm-redis-db-migrate.py — URSM v2 键迁移到专用 Redis db
#
# 用途
#   URSM v2 的节点键目前与网关会话键混在同一个 db（默认 db2，实测 1,585,294 键）。
#   persist writer 每分钟 SCAN <prefix>node:* 采集快照；Redis 的 MATCH 是服务端
#   过滤、游标遍历躲不掉，所以它必须走完整个键空间，实测 12.95~30.40s，正好压在
#   writer 的 30s 预算上，于是
#   "ursm.v2: persist collect failed: redis scan failed: context deadline exceeded"
#   成为常态故障。把 URSM 挪到独立 db 后，SCAN 只遍历约 1.3K 键。
#
# ★★ 为什么必须用本脚本而不是 redis-cli（2026-10-03 实测，redis-cli 6.2.22）
#   redis-cli 的 DUMP|RESTORE 二进制管道在 6.2.22 上**四条路全部失败**，每一
#   条都实测过，不是推断：
#
#   1) `-x restore KEY 0 replace`
#      → ERR syntax error
#      原因：redis-cli 的 -x 永远把 stdin 放在**最后一个**参数，于是命令变成
#      RESTORE key ttl replace <payload>，而正确语法是
#      RESTORE key ttl <payload> [REPLACE]。REPLACE 被放到了 payload 前面。
#
#   2) `--raw dump | head -c -1 | -x restore key <ttl>`
#      → RESTORE 返回 OK，但恢复出的键 TTL 变成 0/1（近乎立即过期），值却正确。
#      原因（MONITOR + Lua #d 实测）：DUMP 的权威长度是 31 字节（CRC64 末字节
#      为 0x09），而 redis-cli --raw 落盘是 32 字节 —— 它把 0x09 做成了 C 风格
#      转义 "\t"（两字节），CRC64 末字节因此错位。head -c -1 只是把错位掩盖。
#
#   3) `--no-raw dump | -x restore key <ttl>`（官方文档的配对写法）
#      → ERR DUMP payload version or checksum are wrong
#      6.2.22 的 -x 并不会把 --no-raw 的转义输出反转义回去。
#
#   4) `MIGRATE 127.0.0.1 <port> KEY 14 500 COPY REPLACE`
#      → IOERR error or timeout reading to target instance
#      原因：MIGRATE 到自身会自锁，官方不支持同实例跨 db 迁移。
#
#   （旁证：Redis Lua 也不能用 —— redis.call('RESTORE', ..., d, ...) 对含 \0 的
#     binary string 直接报 "must be strings or integers"。）
#
#   redis-py 的 dump()/restore() 走的是同一条 Redis 协议，但不做任何字符转义，
#   实测 dump 字节数与 Lua #d 一致（31）、TTL 毫秒级精确保留。因此本脚本用
#   redis-py 实现，不与 redis-cli 的二进制处理纠缠。
#
# ★ 为什么必须在流量静默窗口内做（不要只停 persist writer）
#   ursm:v2:node:* 由**请求路径**每请求写一次：
#     domains/streaming/executors/executor_nodehealth.go:313
#       -> Manager.RecordRequest
#       -> store.RecordRequestKeySet
#       -> record_request.lua
#   停 persist writer 只停掉"读"，停不掉"写"。只要还有请求进来，节点键就会
#   继续落在旧 db；等切到新 db 后，这段窗口内的更新就丢了 —— 而 URSM 靠
#   coverage manifest 重建时只覆盖部分节点（实测 714/1240，缺 563 个旧文法
#   节点），不能靠重启自愈。所以必须先静默流量。
#
# 用法
#   # 1) 只读预检（不写任何数据，可在任意时刻跑）
#   python3 scripts/ursm-redis-db-migrate.py --plan -a 172.16.2.210:6389 -s 2 -d 14
#
#   # 2) 静默窗口内：执行复制 + 逐键校验
#   python3 scripts/ursm-redis-db-migrate.py --copy -a 172.16.2.210:6389 -s 2 -d 14
#
#   # 3) 脚本只**打印**后续动作，不会替你改 env 或重启服务
#
# 参数
#   -a <host:port>   Redis 地址（必填）
#   -s <db>          源 db（必填）
#   -d <db>          目标 db（必填，必须为空）
#   -p <prefix>      键前缀，默认 ursm:v2:
#   -E <path>        取密码的 env 文件，默认 /etc/llm-gateway-go/env
#                    （245 上是 /opt/llm-gateway-go/.env）
#   --batch <n>      pipeline 批大小，默认 500
#
# 运行环境要求
#   python3 >= 3.6 且 `import redis` 可用（245 上是 python3 3.6.8 / redis-py 4.3.6）。
#
# 约束
#   - 密码从 env 文件读，不接受命令行明文传入。
#   - 只迁移 <prefix>* 这一族键，不碰同 db 的其它业务键。
#   - 逐键保留原始 TTL（毫秒精度）；无过期的键恢复为无过期。
#   - 源键一律保留不动（本脚本只 RESTORE，不 DEL 源）—— 回滚只需把
#     URSM_V2_REDIS_DB 置回未设置并重启。
# =====================================================================
import argparse
import os
import sys
import time

try:
    import redis
except ImportError:
    sys.exit("[die] 缺少 redis-py。本脚本需要 `import redis`（245 上已装 4.3.6）。"
             "不要退回 redis-cli 实现，见文件头「为什么必须用本脚本」。")


def die(msg):
    sys.exit("[die] %s" % msg)


def info(msg):
    print("[info] %s" % msg)


def warn(msg):
    print("[warn] %s" % msg, file=sys.stderr)


def read_password(env_file):
    if not os.access(env_file, os.R_OK):
        die("读不到 %s，无法取 Redis 密码（可用 -E <path> 指定；"
            "245 的路径是 /opt/llm-gateway-go/.env）" % env_file)
    with open(env_file, "r") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if line.startswith("LLM_GATEWAY_REDIS_PASSWORD="):
                pw = line.split("=", 1)[1]
                if pw:
                    return pw
    die("%s 里没有 LLM_GATEWAY_REDIS_PASSWORD" % env_file)


def connect(host, port, db, password):
    try:
        cli = redis.Redis(host=host, port=port, db=db, password=password,
                          socket_timeout=60, decode_responses=False)
        cli.ping()
        return cli
    except Exception as exc:  # noqa: BLE001 —— 任何连接异常都必须在预检阶段暴露
        die("连接 %s:%d db%d 失败: %s。先确认地址/端口/密码可达，再谈迁移。" %
            (host, port, db, exc))


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("--plan", action="store_true")
    ap.add_argument("--copy", action="store_true")
    ap.add_argument("-a", dest="addr", required=True)
    ap.add_argument("-s", dest="src_db", required=True, type=int)
    ap.add_argument("-d", dest="dst_db", required=True, type=int)
    ap.add_argument("-p", dest="prefix", default="ursm:v2:")
    ap.add_argument("-E", dest="env_file", default="/etc/llm-gateway-go/env")
    ap.add_argument("--batch", dest="batch", type=int, default=500)
    ap.add_argument("--force", action="store_true",
                    help="允许目标 db 非空（第二遍补复制用；会覆盖同名键）")
    args = ap.parse_args()

    if args.plan == args.copy:
        die("必须且只能给 --plan 或 --copy")
    if args.src_db == args.dst_db:
        die("src db 与 dst db 不能相同")

    if ":" not in args.addr:
        die("-a 必须是 <host:port>，收到: %s" % args.addr)
    host, port_s = args.addr.rsplit(":", 1)
    try:
        port = int(port_s)
    except ValueError:
        die("-a 的端口不是数字: %s" % port_s)

    password = read_password(args.env_file)
    src = connect(host, port, args.src_db, password)
    dst = connect(host, port, args.dst_db, password)

    # ------------------------------------------------------------------
    # 预检
    # ------------------------------------------------------------------
    src_size = src.dbsize()
    dst_size = dst.dbsize()
    info("源 %s db%d (总键 %d) -> 目标 db%d (总键 %d)" %
         (args.addr, args.src_db, src_size, args.dst_db, dst_size))
    info("前缀: %s*" % args.prefix)

    if dst_size != 0 and not args.force:
        die("目标 db%d 非空（%d 键）。拒绝执行：迁移会与既有键混合，"
            "回滚将无法区分来源。请换一个空 db、先人工清理，"
            "或加 --force（仅用于第二遍补复制）。" % (args.dst_db, dst_size))
    if dst_size != 0 and args.force:
        # --force 只为「两遍复制」的第二遍而存在（2026-10-04 用户选定的路径）：
        #   ① 在线复制 db2→db14        ② 切 URSM_V2_REDIS_DB=14 并重启
        #   ③ 再复制一遍补上 ①② 窗口里 db2 侧的变化
        # RESTORE 本身带 replace=True，所以第 ③ 遍会覆盖同名键。
        warn("目标 db%d 非空（%d 键）且指定了 --force：本遍会**覆盖**同名键。" % (args.dst_db, dst_size))
        warn("仅在「第二遍补复制」时使用。第一遍误加了 --force 会让"
             "既有键与迁移键混杂，回滚时无法区分来源。")

    # 目标 db 必须不含任何 ursm 残留（历史踩过的坑：db15 留着陈旧 meta:ready）
    # 第一遍之后 db14 必然带 meta:ready，所以这条在 --force 下只告警不拦。
    if dst.exists(args.prefix.encode() + b"meta:ready"):
        if args.force:
            warn("目标 db%d 已存在 %s meta:ready（第一遍的产物，符合两遍复制预期）"
                 % (args.dst_db, args.prefix))
        else:
            die("目标 db%d 已存在 %s meta:ready，拒绝执行" % (args.dst_db, args.prefix))

    if args.plan:
        info("预检通过。以下为只读信息，未做任何变更：")
        info("  - 目标 db%d 为空，可用作 URSM 专用 db" % args.dst_db)
        info("  - 竞态量级（2026-10-04 实测）：键空间 30s 内变 0.3%，复制约 40s")
        info("    ⇒ 60s 窗口约错过 8 个键的更新；其中 40% 无 TTL，可能长期陈旧")
        info("  - 本次 SCAN 需遍历源 db 全部 %d 键（MATCH 是服务端过滤，躲不掉），"
             "预计数十秒" % src_size)
        info("")
        info("两遍复制步骤（2026-10-04 用户选定，不停机）：")
        info("  1) 在线复制（此时网关照常写 db2）：")
        info("       echo MIGRATE | python3 $0 --copy %s -s %d -d %d" % (args.addr, args.src_db, args.dst_db))
        info("  2) 校验通过后，两台设 URSM_V2_REDIS_DB=%d 并重启" % args.dst_db)
        info("     （留空=沿用共享 client，行为不变）")
        info("  3) 再复制一遍补上第 ② 步窗口内 db2 侧的变化（必须加 --force）：")
        info("       echo MIGRATE | python3 $0 --copy --force %s -s %d -d %d" % (args.addr, args.src_db, args.dst_db))
        info("  4) 确认 persist committed 恢复、row 数与迁移前同量级、")
        info("     且 /opt/scripts/ursm-snapshot-health.sh 连续 2 轮 exit 0")
        info("")
        info("回滚：把 URSM_V2_REDIS_DB 置回未设置并重启即可；旧 db 数据原样保留不动。")
        return 0

    # ------------------------------------------------------------------
    # 复制
    # ------------------------------------------------------------------
    warn("=== 即将复制 %s* ：%s db%d -> db%d ===" % (args.prefix, args.addr, args.src_db, args.dst_db))
    warn("=== 请确认此刻请求流量已静默；否则窗口内的节点更新会丢失 ===")
    ans = input("输入 MIGRATE 确认: ").strip()
    if ans != "MIGRATE":
        die("未确认，已取消")

    t0 = time.time()

    # 1) 扫描：把整个源 db 走一遍，收集前缀键。
    #    这一步是耗时主项（源 db 实测 158 万键）。
    prefix_b = args.prefix.encode()
    keys = []
    scanned = 0
    last_report = t0
    for k in src.scan_iter(count=1000):
        scanned += 1
        if k.startswith(prefix_b):
            keys.append(k)
        now = time.time()
        if now - last_report >= 5:
            print("\r[scan] 已遍历 %d 键，命中前缀 %d 个" % (scanned, len(keys)), end="")
            sys.stdout.flush()
            last_report = now
    print("\r[scan] 已遍历 %d 键，命中前缀 %d 个（耗时 %.1fs）" % (scanned, len(keys), time.time() - t0))

    if not keys:
        die("源 db%d 没有 %s* 键。迁移无意义，请确认 -s/-p 是否正确。" % (args.src_db, args.prefix))

    # 2) 批量取 DUMP + PTTL。用 pipeline 把 N 次往返压成 1 次。
    payloads = {}
    ttls = {}
    for i in range(0, len(keys), args.batch):
        chunk = keys[i:i + args.batch]
        pipe = src.pipeline(transaction=False)
        for k in chunk:
            pipe.dump(k)
            pipe.pttl(k)
        res = pipe.execute()
        for j, k in enumerate(chunk):
            payloads[k] = res[2 * j]
            ttls[k] = res[2 * j + 1]
    info("[dump] 已取 %d 个键的 DUMP/TTL" % len(payloads))

    # 3) 批量 RESTORE。ttl 必须显式给：RESTORE 的 ttl=0 意为「永不过期」，
    #    而负数会被服务端拒（ERR Invalid TTL value, must be >= 0）——
    #    所以「沿用原 TTL」只能由调用方把 pttl 原样传下去。
    copied = failed = 0
    errors = []
    for i in range(0, len(keys), args.batch):
        chunk = keys[i:i + args.batch]
        # raise_on_error=False 是必须的：pipeline 的命令在 execute() 时才真正
        # 发往服务端，而默认行为是任何一个命令报错就抛 ResponseError —— 那会让
        # 脚本带着 traceback 崩溃，而此时目标 db 已经写了一部分键，操作者看不到
        # 「哪些成功、能不能设 URSM_V2_REDIS_DB」这个最关键的信息。
        # 改成 False 后错误会作为该位置的返回值出现，落到下面的 failed 计数。
        pipe = dst.pipeline(transaction=False)
        queued_keys = []
        try:
            for k in chunk:
                payload = payloads[k]
                if payload is None:
                    # 扫描与 dump 之间键已过期（正常：节点键 TTL 15min）
                    continue
                pttl = ttls[k]
                ttl_ms = 0 if pttl is None or pttl < 0 else pttl
                pipe.restore(k, ttl_ms, payload, replace=True)
                queued_keys.append(k)
            # raise_on_error 是 execute() 的参数，不是 pipeline() 的
            # （redis-py 4.3.6 实测：pipeline(raise_on_error=...) 直接
            #   TypeError: unexpected keyword argument）。它是必须的：默认
            #   行为是任一命令报错就抛 ResponseError，那会让脚本带着
            #   traceback 崩溃，而此时目标 db 已写了一部分键，操作者看不到
            #   「哪些成功、能不能设 URSM_V2_REDIS_DB」这个最关键的信息。
            results = pipe.execute(raise_on_error=False)
        except Exception as exc:  # noqa: BLE001 —— 入队/网络级异常
            failed += len(queued_keys)
            errors.append("批次 %d 整体失败: %s" % (i, exc))
            warn("  批次 %d 失败: %s" % (i, exc))
            continue
        # ★ 必须与 queued_keys 对齐，不能与 chunk 对齐：payload 为 None 的键
        #   根本没入队，拿 chunk 去 zip 会让结果整体错位配对。
        for k, res in zip(queued_keys, results):
            # redis-py 在 pipeline 里对 RESTORE 返回 b"OK"（bytes），
            # 非 pipeline 时返回 True。两种都要认 —— 早先只判 res == "OK"
            # 而 decode_responses=False 时拿到的是 b"OK"，b"OK" == "OK" 在
            # Py3 恒为 False，于是「全部成功」被判成「全部失败」。
            # 反过来若把这里写成恒真（如忽略 res），又会变成假成功。
            if res is True or res == b"OK" or res == "OK":
                copied += 1
            else:
                failed += 1
                if len(errors) < 10:
                    errors.append("%s -> %r" % (k.decode("utf-8", "replace"), res))
        done = min(i + args.batch, len(keys))
        print("\r[copy] %d/%d（copied=%d failed=%d）" % (done, len(keys), copied, failed), end="")
        sys.stdout.flush()
    print()

    info("复制完成: copied=%d failed=%d（耗时 %.1fs）" % (copied, failed, time.time() - t0))
    if failed:
        for e in errors:
            warn("  失败: %s" % e)
        die("有 %d 个键复制失败。目标 db 状态未知，**不要**设置 URSM_V2_REDIS_DB，"
            "先排查后重跑。" % failed)

    # ------------------------------------------------------------------
    # 校验：逐键比对存在性 + TTL。计数相等只证明总数对，不证明成员对。
    # ------------------------------------------------------------------
    # ★ 只有**实际入队过**的键才该被校验存在。
    #   扫描与 DUMP 之间会自然过期一批键（payload is None 那一支被 continue 跳过）。
    #   2026-10-04 实测：2,006 键里 4 个在这段时间过期，RESTORE 0 失败，
    #   但校验拿「扫描时的名单」去比对目标，于是报「4 个键在目标不存在」并 die。
    #   那 4 个键**在源 db2 里也已经没有了**（实测 exists=0/0），
    #   是**假警**，不是复制失败。
    #   ⇒ 判据必须只覆盖真的搬过的东西，否则一次成功的迁移会被自己判死。
    moved = [k for k in keys if payloads.get(k) is not None]
    expired_midway = len(keys) - len(moved)
    if expired_midway:
        info("扫描与 DUMP 之间自然过期 %d 个键（未入队，不参与存在性校验）" % expired_midway)

    missing = []
    ttl_bad = []
    for i in range(0, len(moved), args.batch):
        chunk = moved[i:i + args.batch]
        pipe = dst.pipeline(transaction=False)
        for k in chunk:
            pipe.pttl(k)
        for k, pttl in zip(chunk, pipe.execute()):
            if pttl == -2:
                missing.append(k.decode("utf-8", "replace"))
                continue
            want = ttls[k]
            if want is None or want < 0:
                if pttl != -1:
                    ttl_bad.append("%s: 期望无过期, 实际 %dms" % (k.decode("utf-8", "replace"), pttl))
            else:
                # 迁移耗时会让目标 TTL 比源略短，容差 5s
                if pttl <= 0 or abs(pttl - want) > 5000:
                    ttl_bad.append("%s: 源 %dms, 目标 %dms" % (k.decode("utf-8", "replace"), want, pttl))

    info("校验: 目标前缀键 %d 个（入队 %d）" % (dst.dbsize(), len(moved)))
    if missing:
        for m in missing[:10]:
            warn("  目标缺失: %s" % m)
        # ★ 二次确认：目标没有、源也没有 ⇒ 该键是在复制窗口里自然过期的，不是复制失败。
        #   只有「源还在、目标没有」才是真失败。那种必须 die。
        pipe = src.pipeline(transaction=False)
        for m in missing:
            pipe.exists(m.encode("utf-8"))
        still_in_src = [m for m, ex in zip(missing, pipe.execute()) if ex]
        benign = len(missing) - len(still_in_src)
        if benign:
            info("其中 %d 个在源 db%d 也已不存在 ⇒ 判定为复制窗口内自然过期，不算失败"
                 % (benign, args.src_db))
        if still_in_src:
            for m in still_in_src[:10]:
                warn("  源仍在、目标丢失: %s" % m)
            die("校验失败：%d 个键在源存在但目标丢失。不要设置 URSM_V2_REDIS_DB，保持现状并排查。"
                % len(still_in_src))
    if ttl_bad:
        for t in ttl_bad[:10]:
            warn("  TTL 不符: %s" % t)
        die("校验失败：%d 个键的 TTL 与源不符。不要设置 URSM_V2_REDIS_DB，保持现状并排查。" % len(ttl_bad))

    info("校验通过：%d 个键全部存在且 TTL 一致（扫描到 %d，中间自然过期 %d）。"
         % (copied, len(keys), expired_midway))
    info("源 db%d 仍保留全部原键（未做任何 DEL），回滚安全。" % args.src_db)
    return 0


if __name__ == "__main__":
    sys.exit(main())
