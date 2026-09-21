#!/usr/bin/env python3
"""
完整会话元数据评估脚本
评估三项能力：任务分类、标题抽取、会话总结
对比本地模型(Qwen)与云端模型(Claude)
"""
import json
import time
import requests
import sys
from pathlib import Path
from typing import Dict, Any

# 评估提示词模板
CLASSIFICATION_PROMPT = """请分析这段对话并分类任务类型。

对话内容：
{conversation}

请从以下类别中选择最合适的一个：
- coding: 编程/代码相关
- debugging: 调试/排错
- testing: 测试相关
- documentation: 文档编写
- architecture: 架构设计
- deployment: 部署运维
- performance: 性能优化
- security: 安全相关
- data: 数据处理/分析
- summary: 总结/汇总
- other: 其他

只返回类别名称，不要解释。"""

TITLE_PROMPT = """请为这段对话生成一个简洁的标题（10字以内）。

对话内容：
{conversation}

只返回标题文本，不要解释。"""

SUMMARY_PROMPT = """请为这段对话生成一个简洁的摘要（50字以内）。

对话内容：
{conversation}

只返回摘要文本，不要解释。"""

ENDPOINTS = {
    "qwen-baseline": {
        "url": "http://127.0.0.1:8082",
        "model": "",
        "key": ""
    },
    "qwen-dflash2": {
        "url": "http://127.0.0.1:8084",
        "model": "",
        "key": ""
    },
    "claude-sonnet-5": {
        "url": "https://llm.kxpms.cn/v1",
        "model": "claude-sonnet-5",
        "key": "sk-REDACTED-SET-YOUR-GATEWAY-KEY"
    }
}

def format_conversation(sample: Dict[str, Any]) -> str:
    """格式化对话内容"""
    lines = []
    for turn in sample["turns"]:
        if turn.get("user_message"):
            lines.append(f"User: {turn['user_message']}")
        if turn.get("assistant_message"):
            lines.append(f"Assistant: {turn['assistant_message']}")
    return "\n".join(lines)

def call_model(endpoint_name: str, prompt: str, max_tokens: int = 100) -> Dict[str, Any]:
    """调用单个模型"""
    cfg = ENDPOINTS[endpoint_name]
    headers = {"Content-Type": "application/json"}
    if cfg["key"]:
        headers["Authorization"] = f"Bearer {cfg['key']}"
    
    payload = {
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": 0.1
    }
    if cfg["model"]:
        payload["model"] = cfg["model"]
    
    url = cfg["url"]
    if url.endswith("/v1"):
        api_url = f"{url}/chat/completions"
    else:
        api_url = f"{url}/v1/chat/completions"
    
    start = time.time()
    try:
        resp = requests.post(api_url, json=payload, headers=headers, timeout=30)
        latency = time.time() - start
        
        if resp.status_code == 200:
            data = resp.json()
            if "choices" in data:
                content = data["choices"][0]["message"]["content"].strip()
                return {"success": True, "content": content, "latency": latency, "error": None}
        
        return {"success": False, "content": "", "latency": latency, "error": f"HTTP {resp.status_code}"}
    except Exception as e:
        return {"success": False, "content": "", "latency": time.time() - start, "error": str(e)[:100]}

def evaluate_sample(sample_idx: int, sample: Dict[str, Any], endpoints: list) -> Dict[str, Any]:
    """评估单个样本的三项能力"""
    sample_hash = sample["session_hash"]
    conversation = format_conversation(sample)
    
    print(f"\n[{sample_idx+1}] {sample_hash} ({sample['turn_count']}轮, {sample['estimated_runes']}字符)")
    
    result = {
        "sample_hash": sample_hash,
        "sample_idx": sample_idx,
        "conversation_length": sample["estimated_runes"],
        "turn_count": sample["turn_count"],
        "evaluations": {}
    }
    
    for endpoint in endpoints:
        print(f"\n  {endpoint}:")
        endpoint_result = {}
        
        # 1. 任务分类
        sys.stdout.write(f"    分类... ")
        sys.stdout.flush()
        classification_prompt = CLASSIFICATION_PROMPT.format(conversation=conversation[:2000])
        class_res = call_model(endpoint, classification_prompt, max_tokens=20)
        endpoint_result["classification"] = class_res
        if class_res["success"]:
            print(f"✓ {class_res['content'][:30]} ({class_res['latency']:.2f}s)")
        else:
            print(f"✗ {class_res['error'][:50]}")
        
        # 2. 标题抽取
        sys.stdout.write(f"    标题... ")
        sys.stdout.flush()
        title_prompt = TITLE_PROMPT.format(conversation=conversation[:2000])
        title_res = call_model(endpoint, title_prompt, max_tokens=50)
        endpoint_result["title"] = title_res
        if title_res["success"]:
            print(f"✓ {title_res['content'][:40]} ({title_res['latency']:.2f}s)")
        else:
            print(f"✗ {title_res['error'][:50]}")
        
        # 3. 会话总结
        sys.stdout.write(f"    总结... ")
        sys.stdout.flush()
        summary_prompt = SUMMARY_PROMPT.format(conversation=conversation[:2000])
        summary_res = call_model(endpoint, summary_prompt, max_tokens=100)
        endpoint_result["summary"] = summary_res
        if summary_res["success"]:
            print(f"✓ {summary_res['content'][:40]} ({summary_res['latency']:.2f}s)")
        else:
            print(f"✗ {summary_res['error'][:50]}")
        
        result["evaluations"][endpoint] = endpoint_result
    
    return result

def main():
    samples_path = Path("testdata/sessions_100.jsonl")
    if not samples_path.exists():
        print(f"Error: {samples_path} not found")
        return 1
    
    samples = []
    with open(samples_path) as f:
        for line in f:
            samples.append(json.loads(line))
    
    # 选择前30个样本快速测试
    samples = samples[:30]
    endpoints = ["qwen-dflash2", "claude-sonnet-5"]
    
    print(f"评估 {len(samples)} 个样本")
    print(f"端点: {', '.join(endpoints)}")
    print(f"测试项: 任务分类、标题抽取、会话总结")
    print("="*60)
    
    all_results = []
    for idx, sample in enumerate(samples):
        result = evaluate_sample(idx, sample, endpoints)
        all_results.append(result)
    
    # 保存结果
    output_path = Path("testdata/full_evaluation_results.json")
    with open(output_path, "w") as f:
        json.dump(all_results, f, indent=2, ensure_ascii=False)
    
    # 汇总统计
    print("\n" + "="*60)
    print("汇总统计")
    print("="*60)
    
    for endpoint in endpoints:
        print(f"\n{endpoint}:")
        
        for task in ["classification", "title", "summary"]:
            successes = [r for r in all_results 
                        if r["evaluations"][endpoint][task]["success"]]
            errors = len(all_results) - len(successes)
            
            if successes:
                latencies = [r["evaluations"][endpoint][task]["latency"] 
                           for r in successes]
                latencies.sort()
                p50 = latencies[len(latencies)//2]
                avg = sum(latencies) / len(latencies)
                
                print(f"  {task:15s}: {len(successes):2d} 成功, {errors:2d} 失败, "
                      f"P50={p50:.2f}s, Avg={avg:.2f}s")
            else:
                print(f"  {task:15s}: {errors} 失败")
    
    print(f"\n结果已保存到 {output_path}")
    return 0

if __name__ == "__main__":
    sys.exit(main())
