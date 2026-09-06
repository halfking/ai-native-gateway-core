# src/train.py — P2.4 阶段2 基线模型训练
#
# 用法:
#   cd ml-training
#   python -m src.train --config config.yaml
#   python -m src.train --data data/*.parquet --annotations data/annotations.csv
#   python -m src.train --synthetic 5000          # 无生产数据时的冒烟测试
#
# 输出: models/<run_id>/ 下的 model.joblib + metadata.json
#
# 样本权重: 人工标注样本 weight x2（config.data.annotation_label_weight）

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from datetime import datetime, timezone
from typing import Any

import joblib
import numpy as np
from sklearn.ensemble import RandomForestClassifier

from . import evaluate as evaluate_mod
from .data_loader import (
    IS_ANNOTATED_COL,
    LABEL_COL,
    WEIGHT_COL,
    DataLoadError,
    load_training_data,
)
from .feature_pipeline import build_pipeline, select_features, transformed_feature_names
from .synthetic import generate_synthetic_training_data


def build_model(model_cfg: dict[str, Any]) -> RandomForestClassifier:
    """按config构建RandomForest基线（n_estimators=100, max_depth=20）。"""
    if model_cfg.get("type", "random_forest") != "random_forest":
        raise DataLoadError(f"unsupported model type: {model_cfg.get('type')}")
    return RandomForestClassifier(
        n_estimators=int(model_cfg.get("n_estimators", 100)),
        max_depth=model_cfg.get("max_depth") or None,
        min_samples_split=int(model_cfg.get("min_samples_split", 2)),
        min_samples_leaf=int(model_cfg.get("min_samples_leaf", 1)),
        max_features=model_cfg.get("max_features", "sqrt"),
        class_weight=model_cfg.get("class_weight") or None,
        random_state=int(model_cfg.get("random_state", 42)),
        n_jobs=int(model_cfg.get("n_jobs", -1)),
    )


def train_model(cfg: dict[str, Any], loaded: dict[str, Any]) -> dict[str, Any]:
    """训练Pipeline并返回运行产物（不落盘）。"""
    splits = loaded["splits"]
    features_cfg = loaded["feature_columns"]
    model_cfg = cfg.get("model", {})

    X_train = select_features(splits.train, features_cfg)
    y_train = splits.train[LABEL_COL].astype(str)
    weights = splits.train[WEIGHT_COL].to_numpy() if WEIGHT_COL in splits.train.columns else None

    pipeline = build_pipeline(features_cfg, build_model(model_cfg))
    t0 = time.time()
    pipeline.fit(X_train, y_train, model__sample_weight=weights)
    train_seconds = time.time() - t0

    return {
        "pipeline": pipeline,
        "train_seconds": train_seconds,
        "n_train": int(len(splits.train)),
        "n_val": int(len(splits.val)),
        "n_test": int(len(splits.test)),
        "label_classes": sorted(y_train.unique().tolist()),
        "feature_names": transformed_feature_names(features_cfg),
    }


def save_run(
    run_dir: str,
    pipeline: Any,
    cfg: dict[str, Any],
    result: dict[str, Any],
    data_files: list[str],
    annotations_path: str,
    metrics: dict[str, Any],
) -> str:
    """保存model.joblib + metadata.json到run目录。"""
    os.makedirs(run_dir, exist_ok=True)
    joblib.dump(pipeline, os.path.join(run_dir, "model.joblib"))

    metadata = {
        "created_at": datetime.now(timezone.utc).isoformat(),
        "pipeline_version": "p2.4-phase2",
        "schema_version": "v1",
        "label_column": cfg.get("label", {}).get("column", "chosen_model"),
        "feature_columns": cfg.get("features", {}),
        "label_classes": result["label_classes"],
        "n_features": len(result["feature_names"]),
        "data_files": data_files,
        "annotations_path": annotations_path,
        "split": {"train": result["n_train"], "val": result["n_val"],
                  "test": result["n_test"]},
        "model_params": pipeline.named_steps["model"].get_params(),
        "train_seconds": round(result["train_seconds"], 2),
        "metrics": metrics,
        "config": cfg,
    }
    with open(os.path.join(run_dir, "metadata.json"), "w", encoding="utf-8") as fh:
        json.dump(metadata, fh, ensure_ascii=False, indent=2)
    return run_dir


def run(cfg: dict[str, Any], run_dir: str | None = None,
        loaded: dict[str, Any] | None = None) -> dict[str, Any]:
    """完整训练+评估+保存流程，返回run信息。"""
    if loaded is None:
        loaded = load_training_data(cfg)

    data_cfg = cfg.get("data", {})
    data_files: list[str] = []
    for p in data_cfg.get("parquet_paths", []):
        if os.path.isdir(p):
            import glob as _glob
            data_files.extend(sorted(_glob.glob(os.path.join(p, "**", "*.parquet"),
                                                recursive=True)))
        else:
            data_files.append(str(p))
    annotations_path = data_cfg.get("annotations_path") or ""

    result = train_model(cfg, loaded)
    pipeline = result["pipeline"]
    splits = loaded["splits"]

    metrics = evaluate_mod.compute_metrics(
        pipeline, splits, features_cfg=loaded["feature_columns"],
        rule_engine_accuracy=cfg.get("evaluation", {}).get("rule_engine_accuracy"),
        random_seed=int(cfg.get("evaluation", {}).get("random_baseline_seed", 42)),
        top_k=int(cfg.get("evaluation", {}).get("top_k_features", 20)),
    )
    metrics["train_seconds"] = round(result["train_seconds"], 2)

    if run_dir is None:
        run_id = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
        run_dir = os.path.join("models", run_id)
    save_run(run_dir, pipeline, cfg, result, data_files, annotations_path, metrics)
    evaluate_mod.save_reports(metrics, run_dir)

    return {"run_dir": run_dir, "result": result, "metrics": metrics,
            "loaded": loaded}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m src.train",
        description="P2.4 RandomForest基线训练（AUTO路由）",
    )
    parser.add_argument("--config", default="config.yaml", help="训练配置文件")
    parser.add_argument("--data", nargs="*", default=None,
                        help="Parquet路径（目录/glob/文件），覆盖config")
    parser.add_argument("--annotations", default=None,
                        help="人工标注CSV，覆盖config")
    parser.add_argument("--output-dir", default=None,
                        help="模型输出目录（默认models/<时间戳>）")
    parser.add_argument("--synthetic", type=int, default=None, metavar="N",
                        help="冒烟模式：生成N条合成数据训练，不读Parquet")
    args = parser.parse_args(argv)

    cfg = load_config_wrapper(args.config)

    if args.synthetic:
        # 冒烟模式：合成数据替换Parquet加载（标注合并/清洗/切分逻辑全走真实路径）
        syn = generate_synthetic_training_data(n_rows=int(args.synthetic), seed=7)
        parquet_path = os.path.abspath("data/synthetic_smoke.parquet")
        os.makedirs(os.path.dirname(parquet_path), exist_ok=True)
        syn.to_parquet(parquet_path, index=False)
        cfg.setdefault("data", {})["parquet_paths"] = [parquet_path]
        print(f"[smoke] synthetic data: {len(syn)} rows -> {parquet_path}")

    if args.data:
        cfg.setdefault("data", {})["parquet_paths"] = args.data
    if args.annotations is not None:
        cfg.setdefault("data", {})["annotations_path"] = args.annotations

    try:
        out = run(cfg, run_dir=args.output_dir)
    except DataLoadError as exc:
        print(f"[error] {exc}", file=sys.stderr)
        return 2

    m = out["metrics"]
    print("=" * 62)
    print(f"run dir        : {out['run_dir']}")
    print(f"train/val/test : {out['result']['n_train']}/{out['result']['n_val']}"
          f"/{out['result']['n_test']}")
    print(f"train time     : {out['result']['train_seconds']:.1f}s")
    print(f"model accuracy : {m['accuracy']['model_test']:.4f}")
    print(f"  most_frequent: {m['baselines']['most_frequent']:.4f}"
          f"  random: {m['baselines']['random_uniform']:.4f}")
    rule = m["baselines"].get("rule_engine")
    if rule is not None:
        print(f"  rule_engine  : {rule:.4f}")
    print("=" * 62)
    print(f"models saved   : {os.path.join(out['run_dir'], 'model.joblib')}")
    return 0


def load_config_wrapper(path: str) -> dict[str, Any]:
    from .data_loader import load_config
    return load_config(path)


if __name__ == "__main__":
    raise SystemExit(main())
