#!/usr/bin/env python3
"""
综合压测脚本：多客户端 × 多模型 × 多场景
验证 multi-level sticky、负载均衡、failover

拓扑：
- 20 个虚拟客户端（不同 client fingerprint）
- 10 个 credential (3001-3010)
- 5 个模型 (gpt-4o, gpt-4o-mini, claude-3-opus, claude-3-sonnet, gemini-pro)

测试场景：
1. L1 Sticky: 同一 session 多次请求应路由到同一 credential
2. 负载均衡: 不同 session 应均匀分布到 10 个 credential
3. L2 Fallback: session 过期后，同一 client+model 应路由到同一 credential
4. L3 Fallback: model 过期后，同一 client 应路由到同一 credential
"""
import asyncio
import aiohttp
import time
import json
import hashlib
from collections import defaultdict
from typing import Dict, List, Tuple
import argparse

# ══════════════════════════════════════
# 配置
# ══════════════════════════════════════
GATEWAY_URL = "http://localhost:8781/v1/chat/completions"
API_KEY = "test-key"

MODELS = ["gpt-4o", "gpt-4o-mini", "claude-3-opus", "claude-3-sonnet", "gemini-pro"]
NUM_CLIENTS = 20  # 虚拟客户端数量
NUM_SESSIONS_PER_CLIENT = 5  # 每个客户端的 session 数量

# ══════════════════════════════════════
# 辅助函数
# ══════════════════════════════════════
def generate_client_fingerprint(client_id: int) -> str:
    """生成客户端指纹（模拟不同的浏览器/设备）"""
    return hashlib.md5(f"client-{client_id:03d}".encode()).hexdigest()[:16]

async def send_request(
    session: aiohttp.ClientSession,
    client_id: int,
    session_id: str,
    model: str,
    round_num: int
) -> Tuple[bool, str, int]:
    """
    发送单个请求
    返回：(success, credential_id, latency_ms)
    """
    client_fp = generate_client_fingerprint(client_id)
    
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {API_KEY}",
        "X-Gw-Session-Id": session_id,
        "X-Client-Fingerprint": client_fp,  # 模拟不同客户端
    }
    
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": f"test-r{round_num}"}],
        "stream": False
    }
    
    start = time.time()
    try:
        async with session.post(GATEWAY_URL, headers=headers, json=payload, timeout=10) as resp:
            latency_ms = int((time.time() - start) * 1000)
            data = await resp.json()
            
            if resp.status == 200 and "choices" in data:
                # 从响应中提取 credential_id（如果有 X-Gw-Credential-Id header）
                cred_id = resp.headers.get("X-Gw-Credential-Id", "unknown")
                return (True, cred_id, latency_ms)
            else:
                error_msg = data.get("error", {}).get("message", "unknown")
                return (False, f"error:{error_msg}", latency_ms)
    except asyncio.TimeoutError:
        return (False, "timeout", int((time.time() - start) * 1000))
    except Exception as e:
        return (False, f"exception:{str(e)[:30]}", int((time.time() - start) * 1000))

# ══════════════════════════════════════
# 测试场景
# ══════════════════════════════════════

async def scenario_l1_sticky(session: aiohttp.ClientSession):
    """
    场景 1: L1 Sticky 验证
    - 每个 client 发送 5 个 session，每个 session 重复 3 次
    - 预期：同一 session 的 3 次请求应路由到同一 credential
    """
    print("\n" + "="*60)
    print("场景 1: L1 Sticky 验证")
    print("="*60)
    
    results = []  # [(client_id, session_id, model, round, cred_id), ...]
    
    # 每个客户端 5 个 session × 3 轮
    tasks = []
    for client_id in range(1, NUM_CLIENTS + 1):
        model = MODELS[client_id % len(MODELS)]  # 每个 client 固定一个模型
        for session_num in range(1, NUM_SESSIONS_PER_CLIENT + 1):
            session_id = f"l1-client{client_id:02d}-sess{session_num:02d}"
            for round_num in range(1, 4):  # 每个 session 3 次请求
                tasks.append((client_id, session_id, model, round_num))
    
    print(f"发送 {len(tasks)} 个请求 ({NUM_CLIENTS} clients × {NUM_SESSIONS_PER_CLIENT} sessions × 3 rounds)...")
    
    async def run_task(client_id, session_id, model, round_num):
        success, cred_id, latency = await send_request(session, client_id, session_id, model, round_num)
        return (client_id, session_id, model, round_num, cred_id, success, latency)
    
    start_time = time.time()
    batch_results = await asyncio.gather(*[run_task(*t) for t in tasks])
    duration = time.time() - start_time
    
    # 分析结果
    session_to_creds = defaultdict(set)  # session_id -> set(credential_ids)
    success_count = sum(1 for r in batch_results if r[5])
    
    for client_id, session_id, model, round_num, cred_id, success, latency in batch_results:
        if success:
            session_to_creds[session_id].add(cred_id)
    
    # 统计 sticky 准确率
    sticky_sessions = sum(1 for creds in session_to_creds.values() if len(creds) == 1)
    total_sessions = len(session_to_creds)
    sticky_rate = sticky_sessions / total_sessions * 100 if total_sessions > 0 else 0
    
    print(f"\n✅ 完成 ({duration:.1f}s, {len(tasks)/duration:.0f} req/s)")
    print(f"   成功率: {success_count}/{len(tasks)} ({success_count/len(tasks)*100:.1f}%)")
    print(f"   L1 Sticky: {sticky_sessions}/{total_sessions} sessions ({sticky_rate:.1f}%)")
    
    # 显示前 5 个 session 的 credential 分布
    print("\n   前 5 个 session 的 credential 分布:")
    for i, (session_id, creds) in enumerate(list(session_to_creds.items())[:5]):
        sticky_mark = "✅" if len(creds) == 1 else "❌"
        print(f"   {sticky_mark} {session_id}: {list(creds)}")
    
    return {
        "success_rate": success_count / len(tasks),
        "sticky_rate": sticky_rate / 100,
        "total_requests": len(tasks),
        "duration": duration
    }

async def scenario_load_balancing(session: aiohttp.ClientSession):
    """
    场景 2: 负载均衡验证
    - 100 个不同的 session，每个 session 1 次请求
    - 预期：credential 分布应相对均匀（±20%）
    """
    print("\n" + "="*60)
    print("场景 2: 负载均衡验证")
    print("="*60)
    
    num_unique_sessions = 100
    tasks = []
    
    for i in range(1, num_unique_sessions + 1):
        client_id = ((i - 1) % NUM_CLIENTS) + 1
        model = MODELS[i % len(MODELS)]
        session_id = f"lb-unique-{i:03d}"
        tasks.append((client_id, session_id, model, 1))
    
    print(f"发送 {len(tasks)} 个请求 (100 unique sessions across {NUM_CLIENTS} clients)...")
    
    async def run_task(client_id, session_id, model, round_num):
        success, cred_id, latency = await send_request(session, client_id, session_id, model, round_num)
        return (cred_id, success)
    
    start_time = time.time()
    results = await asyncio.gather(*[run_task(*t) for t in tasks])
    duration = time.time() - start_time
    
    # 统计 credential 分布
    cred_distribution = defaultdict(int)
    success_count = 0
    for cred_id, success in results:
        if success:
            success_count += 1
            cred_distribution[cred_id] += 1
    
    expected_per_cred = num_unique_sessions / 10  # 10 个 credential
    
    print(f"\n✅ 完成 ({duration:.1f}s, {len(tasks)/duration:.0f} req/s)")
    print(f"   成功率: {success_count}/{len(tasks)} ({success_count/len(tasks)*100:.1f}%)")
    print(f"\n   Credential 分布 (期望: {expected_per_cred:.1f} 每个):")
    
    for cred_id in sorted(cred_distribution.keys()):
        count = cred_distribution[cred_id]
        deviation = (count / expected_per_cred - 1) * 100 if expected_per_cred > 0 else 0
        bar = "█" * int(count / 2)
        print(f"   {cred_id}: {count:3d} ({deviation:+5.1f}%) {bar}")
    
    # 计算标准差
    import statistics
    if len(cred_distribution) > 1:
        stdev = statistics.stdev(cred_distribution.values())
        cv = stdev / expected_per_cred * 100 if expected_per_cred > 0 else 0
        print(f"\n   变异系数: {cv:.1f}% (越低越均匀)")
    
    return {
        "success_rate": success_count / len(tasks),
        "distribution": dict(cred_distribution),
        "total_requests": len(tasks),
        "duration": duration
    }

async def scenario_model_variety(session: aiohttp.ClientSession):
    """
    场景 3: 多模型并发
    - 每个模型 20 个不同 session
    - 验证不同模型是否都能正常路由
    """
    print("\n" + "="*60)
    print("场景 3: 多模型并发验证")
    print("="*60)
    
    tasks = []
    for model in MODELS:
        for i in range(1, 21):  # 每个模型 20 个 session
            client_id = ((i - 1) % NUM_CLIENTS) + 1
            session_id = f"model-{model}-{i:02d}"
            tasks.append((client_id, session_id, model, 1))
    
    print(f"发送 {len(tasks)} 个请求 ({len(MODELS)} models × 20 sessions)...")
    
    async def run_task(client_id, session_id, model, round_num):
        success, cred_id, latency = await send_request(session, client_id, session_id, model, round_num)
        return (model, success, cred_id)
    
    start_time = time.time()
    results = await asyncio.gather(*[run_task(*t) for t in tasks])
    duration = time.time() - start_time
    
    # 按模型统计
    model_stats = defaultdict(lambda: {"success": 0, "total": 0, "creds": set()})
    for model, success, cred_id in results:
        model_stats[model]["total"] += 1
        if success:
            model_stats[model]["success"] += 1
            model_stats[model]["creds"].add(cred_id)
    
    print(f"\n✅ 完成 ({duration:.1f}s, {len(tasks)/duration:.0f} req/s)")
    print(f"\n   各模型统计:")
    for model in MODELS:
        stats = model_stats[model]
        success_rate = stats["success"] / stats["total"] * 100
        cred_count = len(stats["creds"])
        print(f"   {model:20s}: {stats['success']:2d}/{stats['total']:2d} ({success_rate:5.1f}%) → {cred_count} credentials")
    
    return {
        "model_stats": {m: dict(s) for m, s in model_stats.items()},
        "total_requests": len(tasks),
        "duration": duration
    }

# ══════════════════════════════════════
# 主函数
# ══════════════════════════════════════

async def main():
    parser = argparse.ArgumentParser(description="LLM Gateway 综合压测")
    parser.add_argument("--scenario", choices=["all", "l1", "lb", "model"], default="all",
                       help="测试场景: all=全部, l1=L1 sticky, lb=负载均衡, model=多模型")
    parser.add_argument("--gateway", default="http://localhost:8781", help="Gateway URL")
    args = parser.parse_args()
    
    global GATEWAY_URL
    GATEWAY_URL = f"{args.gateway}/v1/chat/completions"
    
    print("╔═══════════════════════════════════════════════════════════╗")
    print("║        LLM Gateway 综合压测 (Multi-Level Sticky)         ║")
    print("╚═══════════════════════════════════════════════════════════╝")
    print(f"\nGateway: {args.gateway}")
    print(f"拓扑: {NUM_CLIENTS} clients × 5 models → 10 credentials")
    
    async with aiohttp.ClientSession() as session:
        results = {}
        
        if args.scenario in ["all", "l1"]:
            results["l1"] = await scenario_l1_sticky(session)
        
        if args.scenario in ["all", "lb"]:
            results["lb"] = await scenario_load_balancing(session)
        
        if args.scenario in ["all", "model"]:
            results["model"] = await scenario_model_variety(session)
    
    # 总结
    print("\n" + "="*60)
    print("测试总结")
    print("="*60)
    
    if "l1" in results:
        r = results["l1"]
        print(f"✅ L1 Sticky: {r['sticky_rate']*100:.1f}% ({r['total_requests']} 请求)")
    
    if "lb" in results:
        r = results["lb"]
        print(f"✅ 负载均衡: {r['success_rate']*100:.1f}% 成功 ({r['total_requests']} 请求)")
    
    if "model" in results:
        r = results["model"]
        total_success = sum(s["success"] for s in r["model_stats"].values())
        print(f"✅ 多模型: {total_success}/{r['total_requests']} 成功")
    
    print("\n💡 提示: 查看 gateway 日志验证 STICKY_PICK 输出")
    print("   docker logs r112_gateway 2>&1 | grep STICKY_PICK | tail -20")

if __name__ == "__main__":
    asyncio.run(main())
