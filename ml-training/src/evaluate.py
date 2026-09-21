# src/evaluate.py — P2.4 阶段2 模型评估
#
# 指标:
#   - 准确率（Accuracy，val + test）
#   - 混淆矩阵（Confusion Matrix）
#   - 分provider的 Precision / Recall / F1
#   - 特征重要性（Feature Importance，RandomForest impurity-based）
#   - 基线对比:
#       * most_frequent  — 永远预测训练集最频繁provider
#       * random_uniform — 均匀随机选择
#       * random_stratified — 按训练集标签分布随机（保真模拟线上分布）
#       * rule_engine    — 测试集中人工标注行的 auto_label vs human_label；
#                          无标注行时回退到 config 的 rule_engine_accuracy
#                          （来自P2.1 annotation_stats 视图）
#
# 用法:
#   python -m src.evaluate --run models/20260907-101500
#   （train.py训练完成后会自动调用compute_metrics并写报告）

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any, Optional

import joblib
import numpy as np
import pandas as pd
from sklearn.metrics import (
    accuracy_score,
    classification_report,
    confusion_matrix,
)

from .data_loader import (
    IS_ANNOTATED_COL,
    LABEL_COL,
    RULE_CHOICE_COL,
    DataLoadError,
    load_config,
    load_training_data,
)
from .feature_pipeline import select_features, transformed_feature_names


# ---------------------------------------------------------------------------
# 基线
# ---------------------------------------------------------------------------

def most_frequent_baseline(y_train: pd.Series, y_test: pd.Series) -> float:
    """永远预测训练集最频繁provider。"""
    top = y_train.value_counts().idxmax()
    return float((y_test == top).mean())


def random_uniform_baseline(y_train: pd.Series, y_test: pd.Series,
                            seed: int = 42) -> float:
    """均匀随机选择provider（固定种子保证可复现）。"""
    rng = np.random.default_rng(seed)
    classes = y_train.unique().tolist()
    pred = rng.choice(classes, size=len(y_test))
    return float((pred == y_test.to_numpy()).mean())


def random_stratified_baseline(y_train: pd.Series, y_test: pd.Series,
                               seed: int = 42) -> float:
    """按训练集标签分布随机抽样（理论期望 = Σ p_i²）。"""
    rng = np.random.default_rng(seed + 1)
    probs = y_train.value_counts(normalize=True)
    pred = rng.choice(probs.index.tolist(), size=len(y_test), p=probs.to_numpy())
    return float((pred == y_test.to_numpy()).mean())


def rule_engine_baseline(test_df: pd.DataFrame,
                         config_accuracy: Optional[float] = None) -> Optional[float]:
    """规则引擎基线。

    优先用测试集中人工标注行本地计算（rule_engine_choice vs 人工标签）；
    无标注行时回退到P2.1生产统计值。两者皆无则返回None（报告N/A）。
    """
    if IS_ANNOTATED_COL in test_df.columns and test_df[IS_ANNOTATED_COL].any():
        subset = test_df[test_df[IS_ANNOTATED_COL]]
        correct = (
            subset[RULE_CHOICE_COL].astype(str).to_numpy()
            == subset[LABEL_COL].astype(str).to_numpy()
        )
        return float(correct.mean())
    if config_accuracy is not None:
        return float(config_accuracy)
    return None


# ---------------------------------------------------------------------------
# 指标计算
# ---------------------------------------------------------------------------

def confusion_matrix_frame(y_true: pd.Series, y_pred: np.ndarray) -> pd.DataFrame:
    labels = sorted(set(y_true) | set(y_pred))
    cm = confusion_matrix(y_true, y_pred, labels=labels)
    return pd.DataFrame(cm, index=[f"true={l}" for l in labels],
                        columns=[f"pred={l}" for l in labels])


def feature_importance_frame(pipeline: Any, features_cfg: dict[str, Any]) -> pd.DataFrame:
    """特征重要性（impurity-based），按重要性降序。

    类别/布尔/数值列与输出列一一对应（OrdinalEncoder按列输出），
    因此无需跨列聚合。
    """
    names = transformed_feature_names(features_cfg)
    importances = pipeline.named_steps["model"].feature_importances_
    if len(names) != len(importances):
        raise DataLoadError(
            f"feature name/importance mismatch: {len(names)} vs {len(importances)}"
        )
    out = (pd.DataFrame({"feature": names, "importance": importances})
           .sort_values("importance", ascending=False)
           .reset_index(drop=True))
    out["importance_pct"] = (out["importance"] * 100).round(2)
    return out


def compute_metrics(
    pipeline: Any,
    splits: Any,
    features_cfg: dict[str, Any],
    rule_engine_accuracy: Optional[float] = None,
    random_seed: int = 42,
    top_k: int = 20,
) -> dict[str, Any]:
    """在val/test集上计算全部指标与基线对比，返回可序列化的dict。"""
    train, val, test = splits.frames

    X_val = select_features(val, features_cfg)
    y_val = val[LABEL_COL].astype(str)
    X_test = select_features(test, features_cfg)
    y_test = test[LABEL_COL].astype(str)
    y_train = train[LABEL_COL].astype(str)

    pred_val = pipeline.predict(X_val)
    pred_test = pipeline.predict(X_test)

    val_acc = float(accuracy_score(y_val, pred_val))
    test_acc = float(accuracy_score(y_test, pred_test))

    cm = confusion_matrix_frame(y_test, pred_test)
    per_class = classification_report(
        y_test, pred_test, output_dict=True, zero_division=0
    )

    fi = feature_importance_frame(pipeline, features_cfg)

    # 人工标注子集上的表现（与人工 ground truth 的一致性）
    annotated_acc: Optional[float] = None
    n_annotated_test = 0
    if IS_ANNOTATED_COL in test.columns:
        ann_mask = test[IS_ANNOTATED_COL].to_numpy(dtype=bool)
        n_annotated_test = int(ann_mask.sum())
        if n_annotated_test > 0:
            annotated_acc = float(accuracy_score(
                y_test.to_numpy()[ann_mask], pred_test[ann_mask]
            ))

    rule_acc = rule_engine_baseline(test, rule_engine_accuracy)
    most_freq_acc = most_frequent_baseline(y_train, y_test)
    random_acc = random_uniform_baseline(y_train, y_test, random_seed)

    metrics = {
        "accuracy": {
            "model_val": val_acc,
            "model_test": test_acc,
            "model_annotated_test": annotated_acc,
            "n_annotated_test": n_annotated_test,
        },
        "baselines": {
            "most_frequent": most_freq_acc,
            "random_uniform": random_acc,
            "random_stratified": random_stratified_baseline(y_train, y_test, random_seed),
            "rule_engine": rule_acc,
        },
        "confusion_matrix": {
            "labels": [c.removeprefix("pred=") for c in cm.columns],
            "matrix": cm.to_numpy().tolist(),
        },
        "per_class": per_class,
        "feature_importance": fi.head(int(top_k)).to_dict(orient="records"),
        "beats_baselines": {
            "vs_most_frequent": bool(test_acc > most_freq_acc),
            "vs_random": bool(test_acc > random_acc),
            "vs_rule_engine": (bool(test_acc > rule_acc) if rule_acc is not None else None),
        },
        "n_test": int(len(y_test)),
    }
    return metrics


# ---------------------------------------------------------------------------
# 报告输出
# ---------------------------------------------------------------------------

def _fmt(v: Any, nd: int = 4) -> str:
    return "N/A" if v is None else f"{v:.{nd}f}"


def render_markdown(metrics: dict[str, Any]) -> str:
    """渲染人类可读的Markdown评估报告。"""
    acc = metrics["accuracy"]
    base = metrics["baselines"]
    beats = metrics["beats_baselines"]
    lines = [
        "# P2.4 模型评估报告",
        "",
        f"- 测试样本数: {metrics['n_test']}"
        f"（其中人工标注 {acc['n_annotated_test']} 条）",
        "",
        "## 准确率",
        "",
        "| 指标 | 值 |",
        "|---|---|",
        f"| 模型 (test) | {acc['model_test']:.4f} |",
        f"| 模型 (val) | {acc['model_val']:.4f} |",
        f"| 模型 (test人工标注子集) | {_fmt(acc['model_annotated_test'])} |",
        f"| 基线: 最频繁provider | {base['most_frequent']:.4f} |",
        f"| 基线: 均匀随机 | {base['random_uniform']:.4f} |",
        f"| 基线: 按分布随机 | {base['random_stratified']:.4f} |",
        f"| 基线: 规则引擎 | {_fmt(base['rule_engine'])} |",
        "",
        "## 基线对比结论",
        "",
        f"- 优于最频繁基线: {'✅' if beats['vs_most_frequent'] else '❌'}",
        f"- 优于随机基线: {'✅' if beats['vs_random'] else '❌'}",
        "- 优于规则引擎: " + (
            "✅" if beats["vs_rule_engine"] else "❌"
            if beats["vs_rule_engine"] is not None else "⚠️ 数据不足（无标注行/未配置）"),
        "",
        "## 分provider Precision / Recall / F1",
        "",
        "| provider | precision | recall | f1 | support |",
        "|---|---|---|---|---|",
    ]
    for label, row in metrics["per_class"].items():
        if label in ("accuracy", "macro avg", "weighted avg"):
            continue
        lines.append(
            f"| {label} | {row['precision']:.3f} | {row['recall']:.3f} "
            f"| {row['f1-score']:.3f} | {int(row['support'])} |"
        )
    for avg in ("macro avg", "weighted avg"):
        row = metrics["per_class"][avg]
        lines.append(
            f"| **{avg}** | {row['precision']:.3f} | {row['recall']:.3f} "
            f"| {row['f1-score']:.3f} | {int(row['support'])} |"
        )

    lines += ["", "## 混淆矩阵（行=真实，列=预测）", ""]
    labels = metrics["confusion_matrix"]["labels"]
    matrix = metrics["confusion_matrix"]["matrix"]
    header = "| " + " | ".join([""] + labels) + " |"
    sep = "|" + "---|" * (len(labels) + 1)
    lines += [header, sep]
    for label, row in zip(labels, matrix):
        lines.append("| " + " | ".join([label] + [str(v) for v in row]) + " |")

    lines += ["", "## 特征重要性 Top",
              "", "| rank | feature | importance | % |", "|---|---|---|---|"]
    for i, row in enumerate(metrics["feature_importance"], 1):
        lines.append(
            f"| {i} | {row['feature']} | {row['importance']:.4f} "
            f"| {row['importance_pct']:.2f} |"
        )
    lines.append("")
    return "\n".join(lines)


def save_reports(metrics: dict[str, Any], run_dir: str) -> dict[str, str]:
    """写 evaluation_report.{json,md} / confusion_matrix.csv / feature_importance.csv。"""
    os.makedirs(run_dir, exist_ok=True)
    paths = {}

    json_path = os.path.join(run_dir, "evaluation_report.json")
    with open(json_path, "w", encoding="utf-8") as fh:
        json.dump(metrics, fh, ensure_ascii=False, indent=2)
    paths["json"] = json_path

    md_path = os.path.join(run_dir, "evaluation_report.md")
    with open(md_path, "w", encoding="utf-8") as fh:
        fh.write(render_markdown(metrics))
    paths["markdown"] = md_path

    labels = metrics["confusion_matrix"]["labels"]
    cm_df = pd.DataFrame(metrics["confusion_matrix"]["matrix"],
                         index=labels, columns=labels)
    cm_path = os.path.join(run_dir, "confusion_matrix.csv")
    cm_df.to_csv(cm_path, index_label="true\\pred")
    paths["confusion_matrix"] = cm_path

    fi_df = pd.DataFrame(metrics["feature_importance"])
    fi_path = os.path.join(run_dir, "feature_importance.csv")
    fi_df.to_csv(fi_path, index=False)
    paths["feature_importance"] = fi_path
    return paths


# ---------------------------------------------------------------------------
# CLI（对已保存的run重新评估/复现报告）
# ---------------------------------------------------------------------------

def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m src.evaluate",
        description="评估已训练的模型（重现相同的数据切分）",
    )
    parser.add_argument("--run", required=True, help="训练run目录（含model.joblib）")
    args = parser.parse_args(argv)

    run_dir = args.run
    model_path = os.path.join(run_dir, "model.joblib")
    meta_path = os.path.join(run_dir, "metadata.json")
    if not os.path.exists(model_path) or not os.path.exists(meta_path):
        print(f"[error] run dir must contain model.joblib and metadata.json: {run_dir}",
              file=sys.stderr)
        return 2

    with open(meta_path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)["config"]

    pipeline = joblib.load(model_path)
    loaded = load_training_data(cfg)
    metrics = compute_metrics(
        pipeline, loaded["splits"], features_cfg=loaded["feature_columns"],
        rule_engine_accuracy=cfg.get("evaluation", {}).get("rule_engine_accuracy"),
        random_seed=int(cfg.get("evaluation", {}).get("random_baseline_seed", 42)),
        top_k=int(cfg.get("evaluation", {}).get("top_k_features", 20)),
    )
    paths = save_reports(metrics, run_dir)

    print(f"model test accuracy : {metrics['accuracy']['model_test']:.4f}")
    print(f"most_frequent       : {metrics['baselines']['most_frequent']:.4f}")
    rule = metrics["baselines"]["rule_engine"]
    if rule is not None:
        print(f"rule_engine         : {rule:.4f}")
    print(f"report              : {paths['markdown']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
