#!/usr/bin/env python3
"""
model=auto 流量回放脚本 - 向 154 环境发送请求，生成训练数据到 252 共享库

目标：生成至少 10,000 条包含多样化特征的 auto_route_selections 记录

特征覆盖：
- prompt_length: 短/中/长文本
- system_prompt: 有/无
- tools_count: 0/1-3/4+
- stream: true/false
- hour_of_day: 0-23
- provider: anthropic/openai/zhipu 等
"""

import asyncio
import httpx
import json
import random
import time
from datetime import datetime
from typing import List, Dict, Any

# 154 环境配置
GATEWAY_URL = "http://10.0.0.154:8080/v1/chat/completions"
SMOKE_KEY = "sk-kaixuan-smoke-c2e0f53eb0"

# 多样化 prompt 模板（覆盖不同长度）
PROMPTS = {
    "short": [
        "你好",
        "今天天气怎么样？",
        "1+1等于几？",
        "翻译：hello",
        "推荐一本书",
    ],
    "medium": [
        "请帮我分析一下当前人工智能技术的发展趋势，特别是大语言模型的应用场景。",
        "我需要设计一个电商系统的数据库表结构，包括用户、商品、订单等核心实体。",
        "解释一下 Python 中装饰器的工作原理，并给出一个实际应用的例子。",
        "如何优化一个高并发的 Web 服务？请从架构、数据库、缓存等多个角度分析。",
        "写一段代码实现二叉树的层序遍历，并分析时间复杂度。",
    ],
    "long": [
        """作为一名资深的软件架构师，请帮我设计一个企业级的微服务系统架构。
        
系统需求：
1. 支持百万级用户的电商平台
2. 需要实现用户认证、商品管理、订单处理、支付集成、物流跟踪等核心功能
3. 要求高可用、高并发、可扩展
4. 需要考虑数据一致性和分布式事务处理

请从以下几个方面详细阐述：
- 整体架构设计（微服务拆分原则）
- 技术选型（语言、框架、中间件）
- 数据存储方案（数据库选型、分库分表策略）
- 缓存策略
- 消息队列应用
- 服务治理和监控
- 安全性考虑""",
        """我正在开发一个大型的 SaaS 平台，需要实现多租户架构。请帮我分析以下几个技术方案的优劣：

方案一：共享数据库 + 租户ID字段隔离
方案二：共享数据库 + Schema隔离
方案三：独立数据库

需要考虑的维度包括：
- 数据隔离性和安全性
- 性能和可扩展性
- 运维成本和复杂度
- 租户定制化能力
- 备份和恢复策略

请针对不同的业务场景给出建议，并说明如何在方案之间进行平滑迁移。""",
        """请详细解释分布式系统中的 CAP 定理，并结合实际案例分析：

1. CAP 定理的核心概念（一致性、可用性、分区容错性）
2. 为什么三者不可兼得？请从理论和工程角度解释
3. 常见的 CP 和 AP 系统案例（如 ZooKeeper、Cassandra、Redis）
4. 在微服务架构中如何权衡 CAP？
5. 最终一致性的实现方案和适用场景
6. 分布式事务的解决方案（2PC、3PC、TCC、Saga）

请结合具体的代码示例和架构图说明。""",
    ],
}

# system prompt 模板
SYSTEM_PROMPTS = [
    None,  # 无 system prompt
    "你是一个专业的技术顾问。",
    "You are a helpful assistant that provides accurate and concise answers.",
    "你是一个擅长代码分析和架构设计的 AI 助手。",
    "请用专业、准确的语言回答问题，必要时提供代码示例。",
]

# tools 模板
TOOLS_CONFIGS = [
    None,  # 无 tools
    [{"type": "function", "function": {"name": "get_weather", "description": "获取天气信息", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}}],
    [
        {"type": "function", "function": {"name": "search_web", "description": "搜索互联网", "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}},
        {"type": "function", "function": {"name": "calculate", "description": "执行数学计算", "parameters": {"type": "object", "properties": {"expression": {"type": "string"}}, "required": ["expression"]}}},
    ],
    [
        {"type": "function", "function": {"name": "query_database", "description": "查询数据库", "parameters": {"type": "object", "properties": {"sql": {"type": "string"}}, "required": ["sql"]}}},
        {"type": "function", "function": {"name": "send_email", "description": "发送邮件", "parameters": {"type": "object", "properties": {"to": {"type": "string"}, "subject": {"type": "string"}, "body": {"type": "string"}}, "required": ["to", "subject", "body"]}}},
        {"type": "function", "function": {"name": "create_task", "description": "创建任务", "parameters": {"type": "object", "properties": {"title": {"type": "string"}, "priority": {"type": "string", "enum": ["low", "medium", "high"]}}, "required": ["title"]}}},
    ],
]


async def send_request(client: httpx.AsyncClient, prompt_length: str, request_id: int) -> Dict[str, Any]:
    """发送单个请求"""
    prompt = random.choice(PROMPTS[prompt_length])
    system_prompt = random.choice(SYSTEM_PROMPTS)
    tools = random.choice(TOOLS_CONFIGS)
    stream = random.choice([True, False])
    
    messages = []
    if system_prompt:
        messages.append({"role": "system", "content": system_prompt})
    messages.append({"role": "user", "content": prompt})
    
    payload = {
        "model": "auto",
        "messages": messages,
        "stream": stream,
    }
    
    if tools:
        payload["tools"] = tools
    
    try:
        start_time = time.time()
        response = await client.post(
            GATEWAY_URL,
            json=payload,
            headers={
                "Authorization": f"Bearer {SMOKE_KEY}",
                "Content-Type": "application/json",
            },
            timeout=60.0,
        )
        
        elapsed = time.time() - start_time
        
        if response.status_code == 200:
            if stream:
                # 流式响应：读取完整内容
                content = response.text
                lines = [l for l in content.split("\n") if l.strip() and l.startswith("data: ")]
                return {
                    "success": True,
                    "request_id": request_id,
                    "prompt_length": prompt_length,
                    "has_system": system_prompt is not None,
                    "tools_count": len(tools) if tools else 0,
                    "stream": stream,
                    "elapsed": elapsed,
                    "chunks": len(lines),
                }
            else:
                data = response.json()
                return {
                    "success": True,
                    "request_id": request_id,
                    "prompt_length": prompt_length,
                    "has_system": system_prompt is not None,
                    "tools_count": len(tools) if tools else 0,
                    "stream": stream,
                    "elapsed": elapsed,
                    "model": data.get("model", "unknown"),
                }
        else:
            return {
                "success": False,
                "request_id": request_id,
                "status_code": response.status_code,
                "error": response.text[:200],
            }
    except Exception as e:
        return {
            "success": False,
            "request_id": request_id,
            "error": str(e)[:200],
        }


async def replay_traffic(target_count: int = 10000, batch_size: int = 50, delay_ms: int = 100):
    """回放流量"""
    print(f"=== model=auto 流量回放 ===")
    print(f"目标记录数: {target_count}")
    print(f"并发批次: {batch_size}")
    print(f"批次间延迟: {delay_ms}ms")
    print(f"目标环境: {GATEWAY_URL}")
    print(f"开始时间: {datetime.now().isoformat()}")
    print()
    
    stats = {
        "total": 0,
        "success": 0,
        "failed": 0,
        "by_length": {"short": 0, "medium": 0, "long": 0},
    }
    
    async with httpx.AsyncClient() as client:
        request_id = 0
        while stats["total"] < target_count:
            # 生成一个批次的请求
            tasks = []
            for _ in range(min(batch_size, target_count - stats["total"])):
                # 按比例分配 prompt 长度：50% short, 35% medium, 15% long
                rand = random.random()
                if rand < 0.5:
                    prompt_length = "short"
                elif rand < 0.85:
                    prompt_length = "medium"
                else:
                    prompt_length = "long"
                
                tasks.append(send_request(client, prompt_length, request_id))
                request_id += 1
            
            # 并发执行批次
            results = await asyncio.gather(*tasks, return_exceptions=True)
            
            # 统计结果
            for result in results:
                if isinstance(result, Exception):
                    stats["failed"] += 1
                    print(f"✗ Exception: {result}")
                elif result.get("success"):
                    stats["success"] += 1
                    stats["by_length"][result["prompt_length"]] += 1
                    if stats["success"] % 100 == 0:
                        print(f"✓ {stats['success']:,} / {target_count:,} ({stats['success']*100/target_count:.1f}%) | "
                              f"short={stats['by_length']['short']} medium={stats['by_length']['medium']} long={stats['by_length']['long']}")
                else:
                    stats["failed"] += 1
                    print(f"✗ Request {result.get('request_id')}: {result.get('error', result.get('status_code'))}")
            
            stats["total"] = stats["success"] + stats["failed"]
            
            # 批次间延迟
            if stats["total"] < target_count:
                await asyncio.sleep(delay_ms / 1000.0)
    
    print()
    print("=== 回放完成 ===")
    print(f"总请求数: {stats['total']:,}")
    print(f"成功: {stats['success']:,} ({stats['success']*100/stats['total']:.1f}%)")
    print(f"失败: {stats['failed']:,}")
    print(f"按长度分布: short={stats['by_length']['short']:,} medium={stats['by_length']['medium']:,} long={stats['by_length']['long']:,}")
    print(f"结束时间: {datetime.now().isoformat()}")
    
    return stats


if __name__ == "__main__":
    import sys
    
    target = int(sys.argv[1]) if len(sys.argv) > 1 else 10000
    batch = int(sys.argv[2]) if len(sys.argv) > 2 else 50
    delay = int(sys.argv[3]) if len(sys.argv) > 3 else 100
    
    asyncio.run(replay_traffic(target, batch, delay))
