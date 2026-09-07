#!/usr/bin/env python3
"""
clean_label_pollution.py — 标签空间污染清理工具

问题背景：
  data_loader.py 的 merge_annotations() 函数中 annotation_require_known_label
  默认为 False，允许人工标注使用训练数据中不存在的标签（如 provider 名称而非
  model 名称），导致词汇表污染和模型混淆。

功能：
  1. 分析人工标注CSV中标签分布
  2. 识别不在训练数据标签空间的"污染标签"
  3. 生成清理报告和修正建议
  4. 可选：生成清理后的annotations.csv

使用方法：
  # 分析模式（只读，生成报告）
  python scripts/clean_label_pollution.py \\
    --training-data data/training.parquet \\
    --annotations data/annotations.csv \\
    --label-column chosen_model \\
    --report pollution_report.txt

  # 清理模式（生成清理后的annotations.csv）
  python scripts/clean_label_pollution.py \\
    --training-data data/training.parquet \\
    --annotations data/annotations.csv \\
    --label-column chosen_model \\
    --output data/annotations_cleaned.csv \\
    --strategy drop  # 或 map

策略：
  - drop: 删除污染标签行（保守，推荐）
  - map: 尝试映射到已知标签（需提供映射表）

审计追踪：
  所有操作生成详细日志，记录：删除/映射的标签、影响的行数、清理前后分布
"""

from __future__ import annotations

import argparse
import sys
from collections import Counter
from pathlib import Path
from typing import Literal

import pandas as pd


def load_training_labels(parquet_path: str, label_column: str) -> set[str]:
    """从训练数据中提取已知标签词汇表。"""
    df = pd.read_parquet(parquet_path)
    if label_column not in df.columns:
        raise ValueError(f"Label column '{label_column}' not found in training data. "
                         f"Available: {list(df.columns)}")
    labels = set(df[label_column].dropna().unique())
    return labels


def load_annotations(csv_path: str, label_columns: list[str]) -> pd.DataFrame:
    """加载人工标注CSV（llm-gw-annotator格式）。"""
    df = pd.read_csv(csv_path)
    for col in label_columns:
        if col not in df.columns:
            raise ValueError(f"Annotation label column '{col}' not found. "
                             f"Available: {list(df.columns)}")
    return df


def analyze_pollution(
    annotations: pd.DataFrame,
    known_labels: set[str],
    label_column: str,
) -> dict:
    """分析标签污染情况。"""
    ann_labels = annotations[label_column].dropna()
    label_counts = Counter(ann_labels)
    
    polluted = {label: count for label, count in label_counts.items()
                if label not in known_labels and label != ""}
    clean = {label: count for label, count in label_counts.items()
             if label in known_labels}
    
    total_ann = len(ann_labels)
    polluted_count = sum(polluted.values())
    clean_count = sum(clean.values())
    
    return {
        "total_annotations": total_ann,
        "clean_count": clean_count,
        "polluted_count": polluted_count,
        "pollution_rate": polluted_count / total_ann if total_ann > 0 else 0.0,
        "polluted_labels": polluted,
        "clean_labels": clean,
        "known_labels_count": len(known_labels),
    }


def generate_report(analysis: dict, output_path: str | None = None) -> str:
    """生成标签污染分析报告。"""
    lines = [
        "=" * 80,
        "标签空间污染分析报告",
        "=" * 80,
        "",
        f"训练数据已知标签数: {analysis['known_labels_count']}",
        f"人工标注总数: {analysis['total_annotations']}",
        f"  - 干净标注（在已知词汇表内）: {analysis['clean_count']} "
        f"({analysis['clean_count']/analysis['total_annotations']*100:.1f}%)",
        f"  - 污染标注（不在已知词汇表）: {analysis['polluted_count']} "
        f"({analysis['pollution_rate']*100:.1f}%)",
        "",
        "污染标签分布（按出现次数降序）:",
        "-" * 80,
    ]
    
    if analysis['polluted_labels']:
        for label, count in sorted(analysis['polluted_labels'].items(),
                                   key=lambda x: x[1], reverse=True):
            lines.append(f"  {label:40s} : {count:5d} 次")
    else:
        lines.append("  （无污染标签，数据干净）")
    
    lines.extend([
        "",
        "干净标签分布（Top 10）:",
        "-" * 80,
    ])
    
    if analysis['clean_labels']:
        top10 = sorted(analysis['clean_labels'].items(),
                      key=lambda x: x[1], reverse=True)[:10]
        for label, count in top10:
            lines.append(f"  {label:40s} : {count:5d} 次")
    
    lines.extend([
        "",
        "=" * 80,
        "建议操作:",
        "=" * 80,
    ])
    
    if analysis['polluted_count'] == 0:
        lines.append("✓ 数据干净，无需清理。")
    elif analysis['pollution_rate'] < 0.05:
        lines.append(f"⚠ 污染率较低（{analysis['pollution_rate']*100:.1f}%），"
                    f"建议使用 --strategy drop 删除污染行。")
    else:
        lines.append(f"⚠⚠ 污染率较高（{analysis['pollution_rate']*100:.1f}%），"
                    f"需要人工审查标注规范。")
        lines.append("   可能原因：")
        lines.append("   1. 标注者使用了 provider 名称而非 model 名称")
        lines.append("   2. 标注数据与训练数据版本不匹配")
        lines.append("   3. 训练数据中缺少新增模型")
    
    lines.append("")
    report = "\n".join(lines)
    
    if output_path:
        Path(output_path).write_text(report, encoding="utf-8")
        print(f"报告已保存到: {output_path}")
    
    return report


def clean_annotations(
    annotations: pd.DataFrame,
    known_labels: set[str],
    label_column: str,
    strategy: Literal["drop", "map"],
    label_map: dict[str, str] | None = None,
) -> tuple[pd.DataFrame, dict]:
    """清理污染标注。
    
    Returns:
        (cleaned_df, stats)
    """
    df = annotations.copy()
    original_len = len(df)
    
    stats = {
        "original_rows": original_len,
        "strategy": strategy,
        "dropped_rows": 0,
        "mapped_rows": 0,
        "final_rows": 0,
    }
    
    if strategy == "drop":
        # 只保留已知标签的行
        mask = df[label_column].isin(known_labels) | df[label_column].isna()
        df = df[mask]
        stats["dropped_rows"] = original_len - len(df)
    
    elif strategy == "map":
        if not label_map:
            raise ValueError("Strategy 'map' requires --label-map")
        
        # 映射污染标签到已知标签
        for old_label, new_label in label_map.items():
            if new_label not in known_labels:
                print(f"WARNING: 映射目标 '{new_label}' 不在已知标签中，跳过", file=sys.stderr)
                continue
            mask = df[label_column] == old_label
            mapped_count = mask.sum()
            if mapped_count > 0:
                df.loc[mask, label_column] = new_label
                stats["mapped_rows"] += mapped_count
                print(f"  映射 '{old_label}' -> '{new_label}': {mapped_count} 行")
        
        # 删除未映射的污染标签
        mask = df[label_column].isin(known_labels) | df[label_column].isna()
        df = df[mask]
        stats["dropped_rows"] = original_len - len(df) - stats["mapped_rows"]
    
    stats["final_rows"] = len(df)
    return df, stats


def main():
    parser = argparse.ArgumentParser(
        description="标签空间污染清理工具",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("--training-data", required=True,
                        help="训练数据Parquet文件路径")
    parser.add_argument("--annotations", required=True,
                        help="人工标注CSV文件路径")
    parser.add_argument("--label-column", default="chosen_model",
                        help="标签列名（默认: chosen_model）")
    parser.add_argument("--annotation-label-column", default="human_label",
                        help="标注CSV中的标签列名（默认: human_label）")
    parser.add_argument("--report",
                        help="输出分析报告路径（不指定则打印到stdout）")
    parser.add_argument("--output",
                        help="输出清理后的annotations.csv路径（清理模式）")
    parser.add_argument("--strategy", choices=["drop", "map"],
                        help="清理策略: drop=删除污染行, map=映射到已知标签")
    parser.add_argument("--label-map",
                        help="标签映射表（JSON格式），用于strategy=map")
    
    args = parser.parse_args()
    
    # 加载数据
    print(f"加载训练数据: {args.training_data}")
    known_labels = load_training_labels(args.training_data, args.label_column)
    print(f"  已知标签数: {len(known_labels)}")
    
    print(f"加载人工标注: {args.annotations}")
    annotations = load_annotations(args.annotations, [args.annotation_label_column])
    print(f"  标注行数: {len(annotations)}")
    
    # 分析污染
    print("\n分析标签污染...")
    analysis = analyze_pollution(annotations, known_labels, args.annotation_label_column)
    
    # 生成报告
    report = generate_report(analysis, args.report)
    if not args.report:
        print(report)
    
    # 清理（可选）
    if args.output:
        if not args.strategy:
            print("ERROR: --output 需要指定 --strategy", file=sys.stderr)
            sys.exit(1)
        
        print(f"\n执行清理（策略: {args.strategy}）...")
        label_map = None
        if args.label_map:
            import json
            label_map = json.loads(Path(args.label_map).read_text())
        
        cleaned_df, stats = clean_annotations(
            annotations, known_labels, args.annotation_label_column,
            args.strategy, label_map
        )
        
        cleaned_df.to_csv(args.output, index=False)
        print(f"\n清理完成:")
        print(f"  原始行数: {stats['original_rows']}")
        print(f"  删除行数: {stats['dropped_rows']}")
        print(f"  映射行数: {stats['mapped_rows']}")
        print(f"  最终行数: {stats['final_rows']}")
        print(f"  输出文件: {args.output}")
    
    # 退出码
    if analysis['polluted_count'] > 0 and not args.output:
        print("\n⚠ 检测到标签污染，建议执行清理", file=sys.stderr)
        sys.exit(1)
    
    sys.exit(0)


if __name__ == "__main__":
    main()
