#!/usr/bin/env python3
"""
对指定模型的会话进行任务分类，用于修正work_type_model_route
"""
import json
import psycopg2
import requests
import time
from collections import defaultdict

DB_CONFIG = {
    "host": "127.0.0.1",
    "database": "llm_gateway",
    "user": "llm_gateway",
    "password": "4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"
}

CLAUDE_CONFIG = {
    "url": "https://llm.kxpms.cn/v1/chat/completions",
    "model": "claude-sonnet-5",
    "key": "sk-REDACTED-SET-YOUR-GATEWAY-KEY"
}

TASK_CLASSIFICATION_PROMPT = """分析这段对话并分类任务类型。

对话内容：
{conversation}

请从以下任务类型中选择最合适的一个：
- code_gen: 代码生成/编写
- code_review: 代码审查/优化
- reasoning: 推理/分析/解释
- general_chat: 一般对话/闲聊
- long_doc: 长文档处理/分析
- session_summary: 会话总结
- session_title: 会话标题生成
- agent_workflow: 智能体工作流
- meeting_summary: 会议纪要
- image_understand: 图像理解
- test_key: 测试/探活

只返回任务类型的key（如code_gen），不要解释。"""

def load_session_ids(file_path):
    """从文件加载会话ID"""
    sessions = []
    with open(file_path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith('-') or 'client_model' in line:
                continue
            parts = line.split('|')
            if len(parts) >= 2:
                model = parts[0].strip()
                session_id = parts[1].strip()
                if model and session_id and session_id.startswith('gw_'):
                    sessions.append((model, session_id))
    return sessions

def fetch_session_content(conn, tenant_id, session_id):
    """从数据库获取会话内容"""
    with conn.cursor() as cur:
        cur.execute("""
            SELECT request_delta, response_delta
            FROM session_bodies_unified
            WHERE tenant_id = %s AND session_id = %s
            ORDER BY turn_no
            LIMIT 10
        """, (tenant_id, session_id))
        
        rows = cur.fetchall()
        if not rows:
            return None
        
        conversation = []
        for req_delta, resp_delta in rows:
            if req_delta:
                for msg in req_delta:
                    if msg.get('role') == 'user':
                        conversation.append(f"User: {msg.get('content', '')[:500]}")
            if resp_delta:
                for msg in resp_delta:
                    if msg.get('role') == 'assistant':
                        conversation.append(f"Assistant: {msg.get('content', '')[:500]}")
        
        return "\n".join(conversation) if conversation else None

def classify_task(conversation):
    """使用Claude分类任务"""
    prompt = TASK_CLASSIFICATION_PROMPT.format(conversation=conversation[:2000])
    
    try:
        resp = requests.post(
            CLAUDE_CONFIG["url"],
            json={
                "model": CLAUDE_CONFIG["model"],
                "messages": [{"role": "user", "content": prompt}],
                "max_tokens": 50,
                "temperature": 0.1
            },
            headers={
                "Authorization": f"Bearer {CLAUDE_CONFIG['key']}",
                "Content-Type": "application/json"
            },
            timeout=30
        )
        
        if resp.status_code == 200:
            data = resp.json()
            if "choices" in data:
                task_type = data["choices"][0]["message"]["content"].strip().lower()
                # 去除可能的markdown标记
                task_type = task_type.replace('`', '').strip()
                return task_type
        
        return "unknown"
    except Exception as e:
        print(f"  分类错误: {str(e)[:100]}")
        return "error"

def main():
    # 加载会话ID
    print("加载会话ID...")
    sessions = load_session_ids("/tmp/model_sessions.txt")
    print(f"找到 {len(sessions)} 个会话\n")
    
    # 连接数据库
    conn = psycopg2.connect(**DB_CONFIG)
    
    # 分类统计
    model_task_stats = defaultdict(lambda: defaultdict(int))
    results = []
    
    print("开始分类...\n")
    for idx, (model, session_id) in enumerate(sessions, 1):
        print(f"[{idx}/{len(sessions)}] {model} / {session_id[:20]}...")
        
        # 获取会话内容
        conversation = fetch_session_content(conn, 'default', session_id)
        if not conversation:
            print("  跳过：无内容")
            continue
        
        # 分类
        task_type = classify_task(conversation)
        print(f"  任务类型: {task_type}")
        
        # 统计
        model_task_stats[model][task_type] += 1
        results.append({
            "model": model,
            "session_id": session_id,
            "task_type": task_type,
            "conversation_preview": conversation[:200]
        })
        
        # 限流
        time.sleep(0.5)
    
    conn.close()
    
    # 生成报告
    print("\n" + "="*60)
    print("任务分布统计")
    print("="*60)
    
    for model in sorted(model_task_stats.keys()):
        print(f"\n{model}:")
        task_dist = model_task_stats[model]
        total = sum(task_dist.values())
        
        for task_type in sorted(task_dist.keys(), key=lambda x: -task_dist[x]):
            count = task_dist[task_type]
            pct = count / total * 100 if total > 0 else 0
            print(f"  {task_type:20s}: {count:3d} ({pct:5.1f}%)")
    
    # 保存结果
    output_file = "testdata/model_task_classification.json"
    with open(output_file, "w") as f:
        json.dump({
            "summary": dict(model_task_stats),
            "details": results
        }, f, indent=2, ensure_ascii=False)
    
    print(f"\n结果已保存到 {output_file}")

if __name__ == "__main__":
    main()
