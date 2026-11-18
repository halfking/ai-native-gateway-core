#!/usr/bin/env python3
"""
211-auto-analysis.py - 在211服务器上分析auto模式的运行状况

功能：
1. 查询最近24小时的auto请求
2. 分析任务分类分布
3. 检查模型选择准确性
4. 检测零候选重选事件
5. 分析失败模式
6. 生成HTML报告

运行方式：
  在211上执行: python3 211-auto-analysis.py
"""

import subprocess
import json
import sys
from datetime import datetime
from typing import Dict, List, Tuple

def get_secret_key() -> str:
    """从容器环境变量获取密钥"""
    try:
        container = subprocess.check_output(
            ["docker", "ps", "--filter", "name=llm-gateway-211", "-q"],
            text=True
        ).strip().split()[0]
        
        secret = subprocess.check_output(
            ["docker", "exec", container, "printenv", "LLM_GATEWAY_SECRET_KEY"],
            text=True
        ).strip()
        return secret
    except Exception as e:
        print(f"获取密钥失败: {e}")
        return ""

def query_database(sql: str) -> List[Tuple]:
    """执行数据库查询"""
    # 这里需要实际的数据库连接
    # 由于安全限制，我们通过Python脚本在211上执行
    print(f"执行查询:\n{sql}\n")
    return []

def analyze_auto_requests():
    """分析auto请求"""
    print("=" * 60)
    print("Auto模式运行分析报告")
    print("=" * 60)
    print(f"生成时间: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print()
    
    # 1. 基础统计
    print("## 1. 最近24小时Auto请求统计")
    print("-" * 60)
    
    sql_summary = """
    SELECT 
        COUNT(*) as total_requests,
        COUNT(*) FILTER (WHERE success = true) as success_count,
        COUNT(*) FILTER (WHERE success = false) as failure_count,
        ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / NULLIF(COUNT(*), 0), 2) as success_rate,
        COUNT(*) FILTER (WHERE fallback_used = true) as fallback_count,
        COUNT(DISTINCT chosen_model) as distinct_models,
        AVG(confidence_score) as avg_confidence
    FROM auto_route_selections
    WHERE ts > NOW() - INTERVAL '24 hours';
    """
    
    print("SQL:", sql_summary)
    print()
    
    # 2. 任务类型分布
    print("## 2. 任务类型分布")
    print("-" * 60)
    
    sql_task_distribution = """
    SELECT 
        task_type,
        COUNT(*) as count,
        ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage,
        COUNT(*) FILTER (WHERE success = true) as success,
        COUNT(*) FILTER (WHERE fallback_used = true) as with_fallback
    FROM auto_route_selections
    WHERE ts > NOW() - INTERVAL '24 hours'
    GROUP BY task_type
    ORDER BY count DESC;
    """
    
    print("SQL:", sql_task_distribution)
    print()
    
    # 3. 模型选择分布
    print("## 3. 模型选择分布")
    print("-" * 60)
    
    sql_model_distribution = """
    SELECT 
        chosen_model,
        COUNT(*) as count,
        ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) as percentage,
        AVG(confidence_score) as avg_confidence,
        COUNT(*) FILTER (WHERE success = false) as failures
    FROM auto_route_selections
    WHERE ts > NOW() - INTERVAL '24 hours'
    GROUP BY chosen_model
    ORDER BY count DESC
    LIMIT 10;
    """
    
    print("SQL:", sql_model_distribution)
    print()
    
    # 4. 失败分析
    print("## 4. 失败模式分析")
    print("-" * 60)
    
    sql_failures = """
    SELECT 
        failure_detail_code,
        task_type,
        chosen_model,
        COUNT(*) as count
    FROM auto_route_selections
    WHERE ts > NOW() - INTERVAL '24 hours'
        AND success = false
    GROUP BY failure_detail_code, task_type, chosen_model
    ORDER BY count DESC;
    """
    
    print("SQL:", sql_failures)
    print()
    
    # 5. 检查零候选重选
    print("## 5. 零候选重选事件")
    print("-" * 60)
    print("检查日志中的auto_reselect事件...")
    
    try:
        container = subprocess.check_output(
            ["docker", "ps", "--filter", "name=llm-gateway-211", "-q"],
            text=True
        ).strip().split()[0]
        
        reselect_logs = subprocess.check_output(
            ["docker", "logs", "--since", "24h", container],
            text=True, stderr=subprocess.STDOUT
        )
        
        reselect_count = reselect_logs.count("auto_reselect")
        print(f"发现 {reselect_count} 次重选事件")
        
        if reselect_count > 0:
            print("\n最近的重选事件:")
            for line in reselect_logs.split('\n'):
                if 'auto_reselect' in line:
                    print(f"  {line[:200]}")
                    
    except Exception as e:
        print(f"检查日志失败: {e}")
    
    print()
    
    # 6. 性能指标
    print("## 6. 性能指标")
    print("-" * 60)
    
    sql_performance = """
    SELECT 
        PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY rl.latency_ms) as p50_latency,
        PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY rl.latency_ms) as p95_latency,
        PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY rl.latency_ms) as p99_latency,
        AVG(rl.latency_ms) as avg_latency
    FROM auto_route_selections ars
    JOIN request_logs_with_current_month rl ON ars.request_id = rl.request_id
    WHERE ars.ts > NOW() - INTERVAL '24 hours';
    """
    
    print("SQL:", sql_performance)
    print()
    
    # 7. 生成建议
    print("## 7. 优化建议")
    print("-" * 60)
    print("基于以上数据的建议:")
    print("1. 如果成功率 < 98%，需要检查失败原因")
    print("2. 如果fallback使用率 > 10%，说明首选模型经常不可用")
    print("3. 如果某个任务类型失败率高，需要优化该类型的分类规则")
    print("4. 如果P95延迟 > 100ms，需要优化决策性能")
    print()

def generate_html_report():
    """生成HTML格式的报告"""
    html_template = """
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Auto模式分析报告</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; background: #f5f5f5; }
        .container { max-width: 1200px; margin: 0 auto; background: white; padding: 20px; }
        h1 { color: #333; border-bottom: 3px solid #4CAF50; padding-bottom: 10px; }
        h2 { color: #666; margin-top: 30px; }
        table { width: 100%; border-collapse: collapse; margin: 20px 0; }
        th, td { padding: 12px; text-align: left; border-bottom: 1px solid #ddd; }
        th { background-color: #4CAF50; color: white; }
        tr:hover { background-color: #f5f5f5; }
        .metric { display: inline-block; margin: 10px 20px; }
        .metric-value { font-size: 2em; font-weight: bold; color: #4CAF50; }
        .metric-label { color: #666; }
        .success { color: #4CAF50; }
        .warning { color: #ff9800; }
        .error { color: #f44336; }
        .code { background: #f4f4f4; padding: 10px; border-left: 3px solid #4CAF50; margin: 10px 0; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎯 Auto模式运行分析报告</h1>
        <p><strong>生成时间:</strong> {timestamp}</p>
        
        <h2>📊 关键指标</h2>
        <div class="metrics">
            <div class="metric">
                <div class="metric-value">--</div>
                <div class="metric-label">总请求数</div>
            </div>
            <div class="metric">
                <div class="metric-value success">--%</div>
                <div class="metric-label">成功率</div>
            </div>
            <div class="metric">
                <div class="metric-value">--</div>
                <div class="metric-label">使用模型数</div>
            </div>
        </div>
        
        <h2>📈 任务类型分布</h2>
        <p>待填充实际数据...</p>
        
        <h2>🎲 模型选择分布</h2>
        <p>待填充实际数据...</p>
        
        <h2>⚠️ 失败分析</h2>
        <p>待填充实际数据...</p>
        
        <h2>🔄 零候选重选事件</h2>
        <p>待填充实际数据...</p>
        
        <h2>⚡ 性能指标</h2>
        <p>待填充实际数据...</p>
        
        <h2>💡 优化建议</h2>
        <ul>
            <li>基于实际数据生成建议</li>
        </ul>
    </div>
</body>
</html>
    """.format(timestamp=datetime.now().strftime('%Y-%m-%d %H:%M:%S'))
    
    with open('/tmp/auto_analysis_report.html', 'w', encoding='utf-8') as f:
        f.write(html_template)
    
    print("HTML报告已生成: /tmp/auto_analysis_report.html")

def main():
    print("开始分析211上的Auto模式运行状况...")
    print()
    
    try:
        analyze_auto_requests()
        generate_html_report()
        
        print()
        print("=" * 60)
        print("分析完成！")
        print("=" * 60)
        print()
        print("下一步:")
        print("1. 在211上运行SQL查询获取实际数据")
        print("2. 使用实际数据填充HTML报告")
        print("3. 运行: python3 scripts/audit/auto-211-live-verify.py")
        print("4. 持续监控24小时")
        
    except Exception as e:
        print(f"分析失败: {e}", file=sys.stderr)
        return 1
    
    return 0

if __name__ == "__main__":
    sys.exit(main())
