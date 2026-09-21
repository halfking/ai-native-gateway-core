#!/usr/bin/env python3
"""
根据模型任务分类结果，生成work_type_model_route修正SQL
"""
import json
from collections import defaultdict

def load_classification_results(file_path):
    """加载分类结果"""
    with open(file_path) as f:
        data = json.load(f)
    return data['summary'], data['details']

def generate_route_updates(model_task_stats):
    """生成路由更新SQL"""
    
    # 当前work_type_model_route中的任务类型
    valid_task_types = [
        'agent_workflow', 'code_gen', 'code_review', 'general_chat',
        'image_understand', 'long_doc', 'meeting_summary', 'reasoning',
        'session_summary', 'session_title', 'test_key'
    ]
    
    # 为每个模型计算任务分布权重
    model_task_weights = {}
    
    for model, task_dist in model_task_stats.items():
        # 过滤掉test_key和error
        real_tasks = {k: v for k, v in task_dist.items() 
                     if k in valid_task_types and k not in ['test_key', 'error', 'unknown']}
        
        total = sum(real_tasks.values())
        if total == 0:
            continue
        
        # 计算权重（基于频率）
        weights = {}
        for task_type, count in real_tasks.items():
            weight = (count / total) * 10  # 归一化到0-10
            if weight >= 0.5:  # 只保留权重>=0.5的映射
                weights[task_type] = round(weight, 2)
        
        model_task_weights[model] = weights
    
    return model_task_weights

def generate_sql(model_task_weights):
    """生成SQL更新语句"""
    
    sql_statements = []
    
    sql_statements.append("-- 基于真实指定模型会话的任务分布，更新work_type_model_route")
    sql_statements.append("-- 生成时间: 2026-09-06\n")
    
    for model, task_weights in sorted(model_task_weights.items()):
        sql_statements.append(f"\n-- {model} 的任务分布")
        
        for task_type, weight in sorted(task_weights.items(), key=lambda x: -x[1]):
            # 根据权重决定tier
            if weight >= 5.0:
                tier = 'primary'
            elif weight >= 2.0:
                tier = 'secondary'
            else:
                tier = 'fallback'
            
            # 生成UPSERT语句
            sql = f"""
INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, tier, enabled, min_score, task_quality_score)
VALUES ('{task_type}', '{model}', {weight}, '{tier}', true, 0.5, 0.8)
ON CONFLICT (work_type_key, canonical_name) 
DO UPDATE SET 
  weight = EXCLUDED.weight,
  tier = EXCLUDED.tier,
  enabled = true;"""
            
            sql_statements.append(sql.strip())
    
    # 添加验证查询
    sql_statements.append("\n\n-- 验证更新结果")
    sql_statements.append("""
SELECT 
  work_type_key,
  canonical_name,
  tier,
  weight,
  enabled
FROM work_type_model_route
WHERE canonical_name IN ({models})
ORDER BY work_type_key, tier, weight DESC;
""".format(models=', '.join([f"'{m}'" for m in sorted(model_task_weights.keys())])))
    
    return "\n".join(sql_statements)

def main():
    # 加载分类结果
    print("加载分类结果...")
    summary, details = load_classification_results("testdata/model_task_classification.json")
    
    print(f"总样本数: {len(details)}")
    print(f"模型数: {len(summary)}\n")
    
    # 显示任务分布
    print("="*60)
    print("各模型的任务分布")
    print("="*60)
    
    for model in sorted(summary.keys()):
        print(f"\n{model}:")
        task_dist = summary[model]
        total = sum(task_dist.values())
        
        for task_type in sorted(task_dist.keys(), key=lambda x: -task_dist[x]):
            count = task_dist[task_type]
            pct = count / total * 100 if total > 0 else 0
            print(f"  {task_type:20s}: {count:3d} ({pct:5.1f}%)")
    
    # 生成路由权重
    print("\n" + "="*60)
    print("生成路由权重（过滤test_key和error）")
    print("="*60)
    
    model_task_weights = generate_route_updates(summary)
    
    for model, weights in sorted(model_task_weights.items()):
        if weights:
            print(f"\n{model}:")
            for task_type, weight in sorted(weights.items(), key=lambda x: -x[1]):
                tier = 'primary' if weight >= 5.0 else ('secondary' if weight >= 2.0 else 'fallback')
                print(f"  {task_type:20s}: weight={weight:5.2f}, tier={tier}")
    
    # 生成SQL
    sql = generate_sql(model_task_weights)
    
    output_file = "testdata/update_work_type_model_route.sql"
    with open(output_file, "w") as f:
        f.write(sql)
    
    print(f"\n修正SQL已保存到: {output_file}")
    print(f"SQL语句数: {sql.count('INSERT INTO')}")

if __name__ == "__main__":
    main()
