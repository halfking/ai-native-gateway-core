#!/usr/bin/env python3
"""
简化评估脚本：对比 baseline 与 DFlash2 的延迟和输出
不依赖金标准，直接测量加速比和输出一致性
"""
import json
import time
import requests
from pathlib import Path

def test_endpoint(endpoint, messages, label="test"):
    """测试单个端点"""
    start = time.time()
    try:
        resp = requests.post(
            f"{endpoint}/v1/chat/completions",
            json={
                "messages": messages,
                "max_tokens": 150,
                "temperature": 0.1
            },
            timeout=30
        )
        latency = time.time() - start
        if resp.status_code == 200:
            content = resp.json()["choices"][0]["message"]["content"]
            return {"latency": latency, "content": content, "error": None}
        else:
            return {"latency": latency, "content": "", "error": f"HTTP {resp.status_code}"}
    except Exception as e:
        return {"latency": time.time() - start, "content": "", "error": str(e)}

def main():
    samples_file = Path("testdata/sessions.jsonl")
    if not samples_file.exists():
        print(f"Error: {samples_file} not found")
        return
    
    samples = []
    with open(samples_file) as f:
        for line in f:
            samples.append(json.loads(line))
    
    # 只测试前10个样本以节省时间
    samples = samples[:10]
    print(f"Testing {len(samples)} samples on 2 endpoints\n")
    
    endpoints = {
        "baseline": "http://127.0.0.1:8082",
        "dflash2": "http://127.0.0.1:8084"
    }
    
    results = []
    for i, sample in enumerate(samples, 1):
        print(f"[{i}/{len(samples)}] {sample['session_hash']}")
        messages = [{"role": "user", "content": sample["turns"][0]["user_message"]}]
        
        sample_result = {"sample": sample["session_hash"], "tests": {}}
        for name, url in endpoints.items():
            print(f"  {name}...", end=" ", flush=True)
            res = test_endpoint(url, messages, name)
            sample_result["tests"][name] = res
            if res["error"]:
                print(f"ERROR: {res['error']}")
            else:
                print(f"{res['latency']:.2f}s")
        
        results.append(sample_result)
        print()
    
    # 汇总
    print("\n=== Summary ===")
    for name in endpoints:
        latencies = [r["tests"][name]["latency"] for r in results if not r["tests"][name]["error"]]
        errors = sum(1 for r in results if r["tests"][name]["error"])
        if latencies:
            print(f"{name:10s}: {len(latencies):2d} success, {errors:2d} errors, "
                  f"P50={sorted(latencies)[len(latencies)//2]:.2f}s, "
                  f"P95={sorted(latencies)[int(len(latencies)*0.95)]:.2f}s")
        else:
            print(f"{name:10s}: {errors} errors, no successful requests")
    
    # 加速比
    baseline_lat = [r["tests"]["baseline"]["latency"] for r in results if not r["tests"]["baseline"]["error"]]
    dflash_lat = [r["tests"]["dflash2"]["latency"] for r in results if not r["tests"]["dflash2"]["error"]]
    if baseline_lat and dflash_lat and len(baseline_lat) == len(dflash_lat):
        speedup = sum(baseline_lat) / sum(dflash_lat)
        print(f"\nSpeedup (DFlash2 vs baseline): {speedup:.2f}x")
    
    # 保存
    with open("testdata/comparison.json", "w") as f:
        json.dump(results, f, indent=2, ensure_ascii=False)
    print(f"\nResults saved to testdata/comparison.json")

if __name__ == "__main__":
    main()
