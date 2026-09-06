#!/usr/bin/env python3
"""
验证work_type_model_route修正后的auto模式命中率
"""
import psycopg2
import requests
import json
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
    "key": "sk-jybFTc1JlrSUEJQlH9q1RxmkbJeOCQ6fdv2MWrDpTOff0Gh9"
}

def load_validation_sessions():
    """加载验证会话"""
    sessions = []
    with open('/tmp/validation_sessions.txt') as f:
        for line in f:
            line = line.strip()
            if not line or '|' not in line or 'gw_session_id' in line or line.startswith('-'):
                continue
            parts = [p.strip() for p in line.split('|')]
            if len(parts) >= 3 and parts[0].startswith('gw_'):
                sessions.append({
                    'session_id': parts[0],
                    'user_specified_model': parts[1],
                    'preview': parts[2] if len(parts) > 2 else ''
                })
    return sessions

def classify_task(preview_text):
    """使用Claude分类任务类型"""
    if not preview_text or len(preview_text) < 10:
        return 'test_key'
    
    prompt = f"""分析这段请求并分类任务类型。只返回任务类型key，不要解释。

请求内容：{preview_text[:800]}

任务类型：code_gen, code_review, reasoning, general_chat, long_doc, session_summary, session_title, agent_workflow, meeting_summary, image_understand, test_key"""
    
    try:
        resp = requests.post(
            CLAUDE_CONFIG["url"],
            json={
                "model": CLAUDE_CONFIG["model"],
                "messages": [{"role": "user", "content": prompt}],
                "max_tokens": 30,
                "temperature": 0.1
            },
            headers={"Authorization": f"Bearer {CLAUDE_CONFIG['key']}"},
            timeout=20
        )
        
        if resp.status_code == 200:
            task = resp.json()["choices"][0]["message"]["content"].strip().lower()
            return task.replace('`', '').strip()
    except:
        pass
    
    return 'unknown'

def get_auto_route_candidates(conn, task_type):
    """获取auto模式下某任务类型的候选模型（按权重排序）"""
    with conn.cursor() as cur:
        cur.execute("""
            SELECT canonical_name, tier, weight
            FROM work_type_model_route
            WHERE work_type_key = %s
              AND enabled = true
            ORDER BY 
              CASE tier 
                WHEN 'primary' THEN 1
                WHEN 'secondary' THEN 2
                ELSE 3
              END,
              weight DESC
            LIMIT 10
        """, (task_type,))
        
        return [(row[0], row[1], row[2]) for row in cur.fetchall()]

def simulate_auto_mode(task_type, candidates, user_specified_model):
    """模拟auto模式路由，判断是否能选中用户指定的模型"""
    if not candidates:
        return False, None, 'no_candidates'
    
    # 检查用户指定的模型是否在候选列表中
    candidate_models = [c[0] for c in candidates]
    
    if user_specified_model in candidate_models:
        rank = candidate_models.index(user_specified_model) + 1
        tier = candidates[candidate_models.index(user_specified_model)][1]
        return True, rank, tier
    else:
        return False, None, 'not_in_list'

def main():
    print("加载验证会话...")
    sessions = load_validation_sessions()
    print(f"找到 {len(sessions)} 个验证会话\n")
    
    conn = psycopg2.connect(**DB_CONFIG)
    
    results = []
    hit_count = 0
    miss_count = 0
    
    # 按模型统计
    model_stats = defaultdict(lambda: {'total': 0, 'hit': 0, 'miss': 0})
    
    print("开始验证...\n")
    for idx, session in enumerate(sessions[:50], 1):  # 先验证50个
        print(f"[{idx}/50] {session['session_id'][:20]}... {session['user_specified_model']}")
        
        # 分类任务
        task_type = classify_task(session['preview'])
        print(f"  任务类型: {task_type}")
        
        # 获取auto模式候选
        candidates = get_auto_route_candidates(conn, task_type)
        print(f"  候选模型数: {len(candidates)}")
        
        # 模拟auto路由
        is_hit, rank, info = simulate_auto_mode(
            task_type, 
            candidates, 
            session['user_specified_model']
        )
        
        if is_hit:
            hit_count += 1
            model_stats[session['user_specified_model']]['hit'] += 1
            print(f"  ✅ 命中！排名: {rank}, tier: {info}")
        else:
            miss_count += 1
            model_stats[session['user_specified_model']]['miss'] += 1
            print(f"  ❌ 未命中 ({info})")
        
        model_stats[session['user_specified_model']]['total'] += 1
        
        results.append({
            'session_id': session['session_id'],
            'user_specified_model': session['user_specified_model'],
            'task_type': task_type,
            'candidates': [c[0] for c in candidates[:5]],
            'is_hit': is_hit,
            'rank': rank,
            'info': info
        })
        
        print()
    
    conn.close()
    
    # 汇总报告
    print("="*60)
    print("验证结果汇总")
    print("="*60)
    
    total = hit_count + miss_count
    hit_rate = hit_count / total * 100 if total > 0 else 0
    
    print(f"\n总体命中率: {hit_count}/{total} ({hit_rate:.1f}%)")
    print(f"  命中: {hit_count}")
    print(f"  未命中: {miss_count}")
    
    print("\n各模型命中率:")
    for model in sorted(model_stats.keys()):
        stats = model_stats[model]
        model_hit_rate = stats['hit'] / stats['total'] * 100 if stats['total'] > 0 else 0
        print(f"  {model:25s}: {stats['hit']:2d}/{stats['total']:2d} ({model_hit_rate:5.1f}%)")
    
    # 保存结果
    output_file = 'testdata/auto_mode_validation.json'
    with open(output_file, 'w') as f:
        json.dump({
            'summary': {
                'total': total,
                'hit': hit_count,
                'miss': miss_count,
                'hit_rate': hit_rate,
                'model_stats': dict(model_stats)
            },
            'details': results
        }, f, indent=2, ensure_ascii=False)
    
    print(f"\n详细结果已保存到: {output_file}")
    
    # 判断是否达标
    if hit_rate >= 70:
        print(f"\n✅ 命中率 {hit_rate:.1f}% >= 70%，达到预期目标！")
    else:
        print(f"\n⚠️ 命中率 {hit_rate:.1f}% < 70%，需要进一步优化")

if __name__ == "__main__":
    main()
