#!/usr/bin/env python3
"""
云端多模型评估脚本
对比 Claude Sonnet-5, Claude Opus-5, 本地baseline, 本地DFlash2
"""
import json
import time
import requests
import sys
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor, as_completed

ENDPOINTS = {
    "claude-sonnet-5": {
        "url": "https://llm.kxpms.cn/v1",
        "model": "claude-sonnet-5",
        "key": "sk-RZ8dm0zyw0Ab8T3vTWBuuy3TLR5HF0vwcy18UHInLcW1AsDz"
    },
    "claude-opus-5": {
        "url": "https://llm.kxpms.cn/v1",
        "model": "claude-opus-5",
        "key": "sk-jybFTc1JlrSUEJQlH9q1RxmkbJeOCQ6fdv2MWrDpTOff0Gh9"
    },
    "qwen-baseline": {
        "url": "http://127.0.0.1:8082",
        "model": "",
        "key": ""
    },
    "qwen-dflash2": {
        "url": "http://127.0.0.1:8084",
        "model": "",
        "key": ""
    }
}

def call_model(endpoint_name, messages, max_tokens=200, temperature=0.1):
    """调用单个模型"""
    cfg = ENDPOINTS[endpoint_name]
    headers = {"Content-Type": "application/json"}
    if cfg["key"]:
        headers["Authorization"] = f"Bearer {cfg['key']}"
    
    payload = {
        "messages": messages,
        "max_tokens": max_tokens,
        "temperature": temperature
    }
    if cfg["model"]:
        payload["model"] = cfg["model"]
    
    # 修正URL拼接：如果url已包含/v1，不再重复
    url = cfg["url"]
    if url.endswith("/v1"):
        api_url = f"{url}/chat/completions"
    else:
        api_url = f"{url}/v1/chat/completions"
    
    start = time.time()
    try:
        resp = requests.post(
            api_url,
            json=payload,
            headers=headers,
            timeout=60
        )
        latency = time.time() - start
        
        if resp.status_code == 200:
            data = resp.json()
            if "choices" in data:
                content = data["choices"][0]["message"]["content"]
                return {"success": True, "content": content, "latency": latency, "error": None}
        
        return {"success": False, "content": "", "latency": latency, "error": f"HTTP {resp.status_code}: {resp.text[:200]}"}
    except Exception as e:
        return {"success": False, "content": "", "latency": time.time() - start, "error": str(e)[:200]}

def evaluate_sample(sample_idx, sample, gold_label=None):
    """评估单个样本在所有端点上的表现"""
    sample_hash = sample["session_hash"]
    user_msg = sample["turns"][0]["user_message"]
    messages = [{"role": "user", "content": user_msg}]
    
    print(f"[{sample_idx+1}] {sample_hash} ({len(user_msg)} chars)")
    
    results = {}
    for name in ENDPOINTS:
        sys.stdout.write(f"  {name:18s} ... ")
        sys.stdout.flush()
        
        res = call_model(name, messages)
        results[name] = res
        
        if res["success"]:
            print(f"✓ {res['latency']:.2f}s")
        else:
            print(f"✗ {res['error'][:50]}")
    
    return {
        "sample_hash": sample_hash,
        "sample_idx": sample_idx,
        "gold_label": gold_label,
        "results": results
    }

def main():
    # 加载样本
    samples_path = Path("testdata/sessions.jsonl")
    if not samples_path.exists():
        print(f"Error: {samples_path} not found")
        return 1
    
    samples = []
    with open(samples_path) as f:
        for line in f:
            samples.append(json.loads(line))
    
    # 只测试前20个样本
    samples = samples[:20]
    print(f"Evaluating {len(samples)} samples across {len(ENDPOINTS)} endpoints\n")
    
    # 尝试加载金标准
    gold_path = Path("testdata/gold_consensus.jsonl")
    gold_map = {}
    if gold_path.exists():
        with open(gold_path) as f:
            for line in f:
                rec = json.loads(line)
                if rec.get("gold"):
                    gold_map[rec["session_hash"]] = rec["gold"]["label"]
        print(f"Loaded {len(gold_map)} gold labels\n")
    
    # 顺序评估每个样本
    all_results = []
    for idx, sample in enumerate(samples):
        gold = gold_map.get(sample["session_hash"])
        result = evaluate_sample(idx, sample, gold)
        all_results.append(result)
        print()
    
    # 保存结果
    output_path = Path("testdata/multi_model_comparison.json")
    with open(output_path, "w") as f:
        json.dump(all_results, f, indent=2, ensure_ascii=False)
    
    # 汇总统计
    print("\n" + "="*60)
    print("SUMMARY")
    print("="*60)
    
    for name in ENDPOINTS:
        successes = [r for r in all_results if r["results"][name]["success"]]
        errors = len(all_results) - len(successes)
        
        if successes:
            latencies = [r["results"][name]["latency"] for r in successes]
            latencies.sort()
            p50 = latencies[len(latencies)//2]
            p95 = latencies[int(len(latencies)*0.95)] if len(latencies) > 1 else latencies[0]
            
            print(f"{name:18s}: {len(successes):2d} success, {errors:2d} errors, "
                  f"P50={p50:.2f}s, P95={p95:.2f}s")
        else:
            print(f"{name:18s}: {errors} errors, no successful requests")
    
    print(f"\nResults saved to {output_path}")
    return 0

if __name__ == "__main__":
    sys.exit(main())
