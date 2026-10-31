#!/usr/bin/env python3
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.py`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
import os as _gocache_os
import sys as _gocache_sys
_gocache_dir = _gocache_os.path.dirname(_gocache_os.path.abspath(__file__))
while _gocache_dir != "/" and not _gocache_os.path.isfile(
        _gocache_os.path.join(_gocache_dir, "scripts", "lib", "gocache.py")):
    _gocache_dir = _gocache_os.path.dirname(_gocache_dir)
_gocache_lib = _gocache_os.path.join(_gocache_dir, "scripts", "lib")
if _gocache_os.path.isfile(_gocache_os.path.join(_gocache_lib, "gocache.py")):
    _gocache_sys.path.insert(0, _gocache_lib)
    import gocache
    gocache.apply_to_environ("script")
# -*- coding: utf-8 -*-
"""r1039 混拼形态的【端到端 A/B】—— 真二进制、真 HTTP、真模型。

用途
----
把「混拼形态的占位符标记此前扫描器本体也看不见」这条结论，从「代码/判据」层
推到「真实运行」层：

  * 用**同一棵树**、**同一条 go build 命令**构建两个 macOS 二进制：
      - gateway-fixed  = HEAD（含本轮修复）
      - gateway-mutant = HEAD，但把两处门**退回修复前形态**
        （security/sanitize 的两处门 + internal/markerbound 整文件回退到
          f8951959f^；该包的导出 API 已核对**完全一致** ⇒ 可安全编译）
  * 各自起一个实例（8786 / 8785，**串行**——两个网关同时跑会共用同一个
    DB/Redis ⇒ 第二个实例的 /v1 请求被拒 403 ⇒ 那是**环境干扰**，不是被测行为）
  * 打**同一段提示词**、同一个模型，读**客户端收到的 content**

判据
----
客户端收到的 content 里**有没有原始标记词**。两条对照（canonical ASCII 形态）
保证「突变体不是整个管线都坏了」。

用法
----
    cd <repo> && python3 scripts/audit/r1039-mixed-width-e2e-ab.py

前提
----
  * 需本地已有一个在跑的网关实例，其 env 在
    ~/kaixuan/llm-gateway-go/bin/<version>/env（本脚本从那里抄 env）
  * 需一个可用的上游模型（本机现为 glm-5.2）
  * 脚本只读源码、只在 /tmp 下建副本与二进制，**不碰**共享部署根

审计档：docs/audit/2026-10-20-r1039-脱敏阻断与设置界面审计轮.md §7.13.8
"""
#!/usr/bin/env python3
# -*- coding: utf-8 -*-
import json, os, pathlib, shutil, signal, subprocess, sys, tempfile, time, urllib.request

REPO = pathlib.Path(__file__).resolve().parents[2]


def find_live_env() -> pathlib.Path:
    """找出**正在跑**的网关实例的 env 文件。

    纪律：**不猜路径** —— 本机有多个部署根（llm-gateway-go / llm-gateway-go-v2），
    兄弟会话还会在别的根上部署 ⇒ 只看 `bin/current` 软链会读到**别人的**实例。
    ⇒ 按「监听端口的进程 → exe → 同目录的 env」反查；反查不到再退回「最新的 env」。
    """
    import subprocess
    try:
        out = subprocess.run(["lsof", "-nP", "-iTCP:8782", "-sTCP:LISTEN", "-t"],
                             capture_output=True, text=True, timeout=20).stdout.split()
        if out:
            ls = subprocess.run(["lsof", "-p", out[0]], capture_output=True, text=True, timeout=20).stdout
            for line in ls.splitlines():
                if " txt " in line:
                    cand = pathlib.Path(line.split()[-1]).parent / "env"
                    if cand.exists():
                        return cand
    except Exception:
        pass
    cands = []
    for root in pathlib.Path(os.path.expanduser("~/kaixuan")).glob("llm-gateway-go*"):
        cands.extend(root.glob("bin/*/env"))
    if not cands:
        sys.exit("找不到可用的 env 文件：请设 LLM_GATEWAY_ROOT，或先跑起一个网关实例")
    return max(cands, key=lambda f: f.stat().st_mtime)


LIVE_ENV = find_live_env()
MARKER = "ＳENSITIVE：secret：1"          # 混拼：首字母全角，其余 ASCII
SECRET = "MIXEDWIDTH-PROBE-9F3A2B7C"

GOENV = dict(os.environ)
GOENV["PATH"] = os.path.expanduser("~/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.darwin-arm64/bin") + ":" + GOENV.get("PATH", "")
# ★★ r1048 轮（★ 2026-10-29）：★★ 原来的 `GOCACHE=os.path.expanduser("~/sdk/gocache")`
# ★★ 是**仓外的用户目录** ⇒ ⇒ ★★ ⇒ 它会**覆盖掉**前导块刚设好的【专用缓存】
# ★★ ⇒ ⇒ ★★ ⇒ 「文件里有没有专用证据」这个判据**照样绿** ⇒ ⇒ ★★ ⇒ **假绿**
# ★★ ⇒ ⇒ ★★ ⇒ 所以这里删掉那一项（★ 前导块已经**无条件**把 GOCACHE 指向专用目录了）
# ★★ ⇒ ⇒ ★★ ⇒ ⇒ `dict(os.environ)` 天然带上了那个专用目录。
GOENV.update(GOPROXY="https://goproxy.cn,direct", GOMODCACHE=os.path.expanduser("~/sdk/gomodcache"),
             GOTOOLCHAIN="local", CGO_ENABLED="0")


def build(tree: pathlib.Path, out: pathlib.Path) -> None:
    print(f"  构建 {out.name} …", flush=True)
    r = subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(out), "./cmd/gateway"],
                       cwd=tree, env=GOENV, capture_output=True, text=True)
    if r.returncode != 0:
        print(r.stdout[-2000:], r.stderr[-2000:]); sys.exit(2)
    print(f"  ✓ {out.name}  {out.stat().st_size/1e6:.1f} MB")


def patch_to_prefix_mutant(tree: pathlib.Path) -> None:
    """把两处门退回**修复前**的真实形态（M1 等价）：只认「全 ASCII 词」或「全全角词」。"""
    pl = tree / "security/sanitize/placeholder_loose.go"
    hop = tree / "security/sanitize/hop.go"
    p, h = pl.read_text(), hop.read_text()

    new_gate = "if !containsMarkerWordAnyWidth(text) {"
    old_gate = "if !containsFoldCaseWord(text, placeholderMarkerWord) && !containsFullWidthMarkerWord(text) {"
    assert new_gate in p, "foldMarkerWidth 门形状变了"
    p = p.replace(new_gate, old_gate)

    new_body = '''func mayContainMarkerText(text string) bool {
	if strings.ContainsAny(text, "{｛") {
		return true
	}
	return containsMarkerWordAnyWidth(text)
}'''
    old_body = '''func mayContainMarkerText(text string) bool {
	if strings.ContainsAny(text, "{｛") {
		return true
	}
	if containsFoldCaseWord(text, placeholderMarkerWord) {
		return true
	}
	return containsFullWidthMarkerWord(text)
}'''
    assert new_body in h, "mayContainMarkerText 形状变了"
    h = h.replace(new_body, old_body)

    helper = '''
func containsFullWidthMarkerWord(text string) bool {
	for i := 0; i < len(text); {
		j := strings.IndexByte(text[i:], fullWidthBraceLeadByte)
		if j < 0 {
			return false
		}
		i += j
		if matchFullWidthMarkerWordAt(text, i) {
			return true
		}
		i++
	}
	return false
}

func matchFullWidthMarkerWordAt[T ~string | ~[]byte](text T, i int) bool {
	const word = "sensitive"
	if i < 0 || i+3*len(word) > len(text) {
		return false
	}
	for k := 0; k < len(word); k++ {
		letter, ok := fullWidthLetterAt(text, i)
		if !ok || asciiLower(letter) != word[k] {
			return false
		}
		i += 3
	}
	return true
}
'''
    pl.write_text(p + helper)
    hop.write_text(h)

    # markerbound 是本包文法的**刻意副本** ⇒ pre-fix 形态下它也不认混拼。
    # 整文件回退到修复前版本（导出 API 已核对**完全一致** ⇒ 可安全编译）。
    prefix_mb = subprocess.run(["git", "show", "f8951959f^:internal/markerbound/markerbound.go"],
                                cwd=REPO, capture_output=True, text=True, check=True).stdout
    (tree / "internal/markerbound/markerbound.go").write_text(prefix_mb)
    print("  ✓ 突变体：sanitize 两处门 + markerbound 整文件都已退回修复前形态")


def parse_env(path: pathlib.Path) -> dict:
    env = {}
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        env[k.strip()] = v.strip().strip('"').strip("'")
    return env


def start(binary: pathlib.Path, port: int, workdir: pathlib.Path):
    env = parse_env(LIVE_ENV)
    env["LLM_GATEWAY_LISTEN"] = f":{port}"
    env["LLM_GATEWAY_LOG_DIR"] = str(workdir / "logs")
    env["LLM_GATEWAY_LOG_FILE"] = str(workdir / "logs/gateway.log")
    (workdir / "logs").mkdir(parents=True, exist_ok=True)
    out = open(f"/tmp/r1039-ab-{port}.log", "wb")   # 固定路径：finally 之后也能读
    p = subprocess.Popen([str(binary)], cwd=workdir, env=env, stdout=out, stderr=subprocess.STDOUT)
    return p


def wait_up(port: int, proc, timeout=120) -> bool:
    for _ in range(timeout):
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=3) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        if proc.poll() is not None:
            return False
        time.sleep(1)
    return False


def resolve_api_key(env: dict) -> str:
    """API key 取值顺序：① 环境变量 ② release env ③ 明确报错。

    ⚠ **凭据不入仓** —— 仓里绝不出现明文 key（AGENTS.md 的硬要求）。
    """
    for src in (os.environ.get("LLM_GATEWAY_API_KEY", ""), env.get("LLM_GATEWAY_API_KEY", "")):
        if src:
            return src
    sys.exit("没找到 API key：请 export LLM_GATEWAY_API_KEY=<key>（或让 release env 里带 LLM_GATEWAY_API_KEY）")


def probe(port: int, marker: str = None, api_key: str = None) -> str:
    mk = MARKER if marker is None else marker
    body = json.dumps({
        "model": "glm-5.2",
        "messages": [{"role": "user", "content":
            f"我的密钥是 {SECRET}。\n请原样输出下面这一行字符串，一个字符都不要改动：\n{mk}\n只输出那一行。"}],
        "max_tokens": 2000,
    }, ensure_ascii=False).encode()
    req = urllib.request.Request(f"http://127.0.0.1:{port}/v1/chat/completions", data=body,
                                 headers={"Content-Type": "application/json", "Authorization": f"Bearer {api_key}"})
    try:
        with urllib.request.urlopen(req, timeout=240) as r:
            d = json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        return "__HTTPERROR__%d %s" % (e.code, e.read().decode("utf-8", "replace")[:400])
    return (d["choices"][0]["message"].get("content") or "")


def report(name: str, c: str) -> None:
    if c.startswith("__HTTPERROR__"):
        print(f"[{name}] HTTP 错误: {c}")
        return
    has_secret = SECRET in c
    has_word = any(k in c for k in ("SENSITIVE", "ＳＥＮＳＩＴＩＶＥ", "SENSITIVＥ", "ＳENSITIVE"))
    has_mask = "[REDACTED" in c
    print(f"[{name}] 客户端收到的 content = {c!r}")
    print(f"     含真实密钥           : {has_secret}")
    print(f"     含**原始标记词**     : {has_word}   ← 客户端看见 token 就是泄漏")
    print(f"     含遮蔽标记 [REDACTED : {has_mask}")
    print(f"     ⇒ 判定: {'★ 泄漏（标记原样到达客户端）' if has_word else ('已被遮蔽（不泄漏 token）' if has_mask else '未见标记')}")


def main() -> int:
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="r1039-e2e-ab."))
    procs = []
    try:
        fixed_tree = tmp / "fixed"
        fixed_tree.mkdir()
        subprocess.run(["git", "archive", "HEAD"], cwd=REPO, check=True,
                       stdout=subprocess.PIPE).stdout
        # git archive 直接落盘
        subprocess.run(f"git archive HEAD | tar -x -C {fixed_tree}", cwd=REPO, shell=True, check=True)

        mut_tree = tmp / "mutant"
        mut_tree.mkdir()
        subprocess.run(f"git archive HEAD | tar -x -C {mut_tree}", cwd=REPO, shell=True, check=True)
        patch_to_prefix_mutant(mut_tree)

        api_key = resolve_api_key(parse_env(LIVE_ENV))
        print(f"=== env 来源: {LIVE_ENV} ===")

        CACHE = pathlib.Path("/tmp/r1039-ab-binaries")
        CACHE.mkdir(exist_ok=True)
        print("=== 构建两个二进制（同一棵树、同一命令；有缓存则复用）===")
        for tree, name in ((fixed_tree, "gateway-fixed"), (mut_tree, "gateway-mutant")):
            cached = CACHE / name
            if cached.exists():
                shutil.copy2(cached, tmp / name); print(f"  复用缓存 {name}")
            else:
                build(tree, tmp / name); shutil.copy2(tmp / name, cached)

        print("=== 前置核对：二进制里新函数的命中数（固定版应为 2，突变体应为 0）===")
        for name, b, want in (("fixed", tmp / "gateway-fixed", 2), ("mutant", tmp / "gateway-mutant", 0)):
            n = subprocess.run(["bash", "-c", f"strings {b} | grep -c containsMarkerWordAnyWidth || true"],
                               capture_output=True, text=True).stdout.strip()
            print(f"  {name}: containsMarkerWordAnyWidth 命中 {n} （期望 {want}）")
            print(f"     （命中数 = 两个包 sanitize+markerbound 的合计；仅作信息，不作判据）")

        # ⚠ **串行**：两个网关实例**同时**跑会共用同一个 DB/Redis ⇒ 第二个实例的
        # /v1 请求实测被拒（403）⇒ 那是**环境干扰**，不是被测行为 ⇒ 改成「起一个、
        # 探测、停掉、再起下一个」。
        print("=== 串行 A/B（8786=修复版 先，8785=突变体 后）===")
        out = {}
        for binary, port, tag in ((tmp / "gateway-fixed", 8786, "fixed"), (tmp / "gateway-mutant", 8785, "mutant")):
            wd = tmp / tag
            wd.mkdir(exist_ok=True)
            p = start(binary, port, wd)
            procs.append((p, tag, port))
            print(f"  {tag} pid={p.pid} port={port} → ", end="", flush=True)
            ok = wait_up(port, p)
            print("ready" if ok else "FAILED")
            if not ok:
                print(pathlib.Path(f"/tmp/r1039-ab-{port}.log").read_text()[-2000:]); return 3
            time.sleep(3)
            for mk_name, mk in (("混拼  ＳENSITIVE：secret：1", MARKER),
                                ("对照  SENSITIVE:secret:1（canonical ASCII）", "SENSITIVE:secret:1")):
                c = probe(port, mk, api_key)
                out[(tag, mk_name)] = c
                print(f"  ── {tag} / {mk_name}")
                report(tag + " / " + mk_name, c)
            p.send_signal(signal.SIGTERM)
            try: p.wait(timeout=20)
            except Exception: p.kill()
            procs.remove((p, tag, port))
            time.sleep(3)
        print()
        print("=== 2×2 结论表 ===")
        def raw(c):
            return any(k in c for k in ("SENSITIVE", "ＳＥＮＳＩＴＩＶＥ", "SENSITIVＥ", "ＳENSITIVE"))
        mix_name = "混拼  ＳENSITIVE：secret：1"
        can_name = "对照  SENSITIVE:secret:1（canonical ASCII）"
        for tag in ("fixed", "mutant"):
            for mn in (mix_name, can_name):
                c = out.get((tag, mn), "")
                if c.startswith("__HTTPERROR__"):
                    print(f"  {tag:7s} / {mn[:12]:14s} : HTTP 错误 {c[:90]}")
                    continue
                state = "★ 泄漏（原始 token 到客户端）" if raw(c) else ("已遮蔽（不泄漏 token）" if "[REDACTED" in c else "未见标记")
                print(f"  {tag:7s} / {mn[:14]:16s} : {state}   content={c!r}")
        for port in (8785, 8786):
            lg = pathlib.Path(f"/tmp/r1039-ab-{port}.log")
            if lg.exists():
                print(f"--- 实例 {port} 日志尾部 ---")
                print(lg.read_text()[-1200:])
        return 0
    finally:
        for p, tag, port in procs:
            try:
                p.send_signal(signal.SIGTERM); p.wait(timeout=15)
            except Exception:
                try: p.kill()
                except Exception: pass
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
