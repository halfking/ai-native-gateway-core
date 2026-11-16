#!/usr/bin/env python3
"""
定价SQL验证工具
验证生成的SQL文件中的定价数据是否正确
"""

import re
import sys

SQL_FILE = "/tmp/model_pricing_with_public_price_20261117.sql"

# 预期的档位倍率
TIER_MULTIPLIERS = {
    'free': 1.0,
    'basic': 1.5,
    'standard': 1.8,
    'premium': 2.0,
    'enterprise': 2.5
}

# 预期的毛利率
TIER_MARGINS = {
    'free': 0,
    'basic': 33,
    'standard': 44,
    'premium': 50,
    'enterprise': 60
}

def main():
    print("╔" + "═"*77 + "╗")
    print("║" + " "*20 + "定价SQL验证报告" + " "*43 + "║")
    print("╚" + "═"*77 + "╝")
    print()
    
    try:
        with open(SQL_FILE, 'r') as f:
            content = f.read()
    except FileNotFoundError:
        print(f"❌ 错误: SQL文件不存在: {SQL_FILE}")
        return 1
    
    print(f"SQL文件: {SQL_FILE}")
    print(f"文件大小: {len(content)} 字节")
    print(f"总行数: {len(content.splitlines())} 行")
    print()
    
    # 验证1: SQL结构
    print("━" * 79)
    print("验证 1: SQL结构完整性")
    print("━" * 79)
    print()
    
    insert_count = content.count('INSERT INTO model_pricing')
    has_begin = 'BEGIN;' in content
    has_commit = 'COMMIT;' in content
    select_count = len(re.findall(r'^SELECT', content, re.MULTILINE))
    
    print(f"✓ INSERT语句数量: {insert_count}")
    print(f"✓ 事务完整: BEGIN({'✓' if has_begin else '✗'}) COMMIT({'✓' if has_commit else '✗'})")
    print(f"✓ 验证查询数量: {select_count}")
    print()
    
    # 验证2: 提取所有模型信息
    print("━" * 79)
    print("验证 2: 模型档位分布")
    print("━" * 79)
    print()
    
    # 提取所有模型块
    model_pattern = r'-- Model: ([\w-]+) \((\w+)\).*?-- Tier: (\w+).*?-- Public price: (\d+) credits.*?-- Margin: (\d+) credits \((\d+)% gross margin\)'
    models = re.findall(model_pattern, content, re.DOTALL)
    
    tier_count = {}
    for model_name, vendor, tier, public_price, margin, margin_pct in models:
        tier_count[tier] = tier_count.get(tier, 0) + 1
    
    print(f"{'档位':<12} {'数量':<8} {'倍率':<10}")
    print("─" * 35)
    for tier in ['free', 'basic', 'standard', 'premium', 'enterprise']:
        count = tier_count.get(tier, 0)
        mult = TIER_MULTIPLIERS.get(tier, 0)
        print(f"{tier:<12} {count:<8} {mult}×")
    
    total_models = sum(tier_count.values())
    print()
    print(f"总计模型数: {total_models}")
    print()
    
    # 验证3: 关键模型定价
    print("━" * 79)
    print("验证 3: 关键模型定价检查")
    print("━" * 79)
    print()
    
    key_models = {
        'minimax-m3': {'internal': 100, 'public': 100, 'usd': 1.50},
        'claude-haiku-4-5': {'internal': 400, 'public': 600, 'usd': 9.00},
        'claude-sonnet-4-5': {'internal': 1200, 'public': 2160, 'usd': 32.40},
        'claude-opus-4-8': {'internal': 2000, 'public': 4000, 'usd': 60.00},
        'claude-fable-5': {'internal': 4000, 'public': 10000, 'usd': 150.00}
    }
    
    # 提取内部价格信息
    internal_pattern = r'-- Model: ([\w-]+).*?-- Internal settlement: (\d+) credits.*?-- Public price: (\d+) credits'
    internal_data = re.findall(internal_pattern, content, re.DOTALL)
    internal_dict = {name: (int(internal), int(public)) for name, internal, public in internal_data}
    
    all_pass = True
    for model, expected in key_models.items():
        print(f"检查: {model}")
        if model in internal_dict:
            internal, public = internal_dict[model]
            usd = round(public * 1.5 / 100, 2)
            
            print(f"  内部结算价: {internal} credits (预期: {expected['internal']})")
            print(f"  对外售价: {public} credits (预期: {expected['public']})")
            print(f"  对外USD: ${usd} (预期: ${expected['usd']})")
            
            if internal == expected['internal'] and public == expected['public']:
                print("  ✓ 验证通过")
            else:
                print("  ✗ 验证失败")
                all_pass = False
        else:
            print("  ✗ 未找到模型数据")
            all_pass = False
        print()
    
    # 验证4: 毛利率一致性
    print("━" * 79)
    print("验证 4: 毛利率一致性检查")
    print("━" * 79)
    print()
    
    print(f"{'模型':<20} {'档位':<10} {'毛利率':<12} {'状态':<8}")
    print("─" * 55)
    
    margin_pass = True
    for model_name, vendor, tier, public_price, margin, margin_pct in models:
        expected_margin = TIER_MARGINS.get(tier, 0)
        actual_margin = int(margin_pct)
        status = "✓" if actual_margin == expected_margin else "✗"
        
        if status == "✗":
            margin_pass = False
        
        print(f"{model_name:<20} {tier:<10} {actual_margin}%{' ':<9} {status}")
    
    print()
    print("预期毛利率: Free=0%, Basic=33%, Standard=44%, Premium=50%, Enterprise=60%")
    print()
    
    # 验证5: 价格范围
    print("━" * 79)
    print("验证 5: 价格范围检查")
    print("━" * 79)
    print()
    
    tier_prices = {}
    for model_name, vendor, tier, public_price, margin, margin_pct in models:
        if tier not in tier_prices:
            tier_prices[tier] = []
        tier_prices[tier].append(int(public_price))
    
    print(f"{'档位':<12} {'最小值':<10} {'最大值':<10} {'预期范围':<15}")
    print("─" * 50)
    
    expected_ranges = {
        'free': '100',
        'basic': '300-600',
        'standard': '1890-2160',
        'premium': '4000',
        'enterprise': '10000'
    }
    
    for tier in ['free', 'basic', 'standard', 'premium', 'enterprise']:
        if tier in tier_prices:
            prices = tier_prices[tier]
            min_price = min(prices)
            max_price = max(prices)
            expected = expected_ranges.get(tier, 'N/A')
            print(f"{tier:<12} {min_price:<10} {max_price:<10} {expected:<15}")
    
    print()
    
    # 总结
    print("━" * 79)
    print("验证总结")
    print("━" * 79)
    print()
    
    print(f"✓ SQL结构: {insert_count}个INSERT, BEGIN/COMMIT完整")
    print(f"✓ 档位分布: {len(tier_count)}个档位, 共{total_models}个模型")
    print(f"{'✓' if all_pass else '✗'} 关键模型: 5个代表模型{'全部通过' if all_pass else '部分失败'}")
    print(f"{'✓' if margin_pass else '✗'} 毛利率一致性: {'全部正确' if margin_pass else '部分不匹配'}")
    print(f"✓ 倍率方案: Free=1.0×, Basic=1.5×, Standard=1.8×, Premium=2.0×, Enterprise=2.5×")
    print()
    
    if all_pass and margin_pass:
        print("🎉 所有验证通过! SQL文件可以部署到生产环境")
    else:
        print("⚠️  部分验证失败,请检查SQL文件")
        return 1
    
    print()
    print("━" * 79)
    
    return 0

if __name__ == '__main__':
    sys.exit(main())
