# ml-training/scripts/eval_honest.py — 分组诚实评估
#
# 动机: 造数回放中同一 prompt 的重试会产生完全相同的特征向量（同 content_hash）。
# pipeline 的随机切分会把同 hash 的行分到 train 和 test，测试精度因此被"见过
# 的内容"抬高。本脚本按 content_hash 分组重算：
#   - seen 精度:   测试行中 content_hash 也出现在训练集的部分（泄漏区）
#   - unseen 精度: 测试行中 content_hash 完全未见过的部分（诚实泛化区）
# 两个数字都报告；"ML 是否优于规则引擎"的判定以 unseen 为准（保守口径）。
#
# 用法:
#   python scripts/eval_honest.py --run models/real-v1 --data data/training_real.parquet

from __future__ import annotations

import argparse
import json
import os
import sys

import joblib
import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.data_loader import LABEL_COL, load_parquet, merge_annotations, clean_data, split_dataset, load_annotations  # noqa: E402
from src.feature_pipeline import select_features  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser(description="按 content_hash 分组的诚实评估")
    ap.add_argument("--run", required=True, help="训练 run 目录（含 model.joblib + metadata.json）")
    ap.add_argument("--data", default=None, help="Parquet 路径（默认读 metadata 里的 config）")
    args = ap.parse_args()

    run_dir = args.run
    meta_path = os.path.join(run_dir, "metadata.json")
    with open(meta_path, encoding="utf-8") as fh:
        cfg = json.load(fh)["config"]
    if args.data:
        cfg.setdefault("data", {})["parquet_paths"] = [args.data]

    pipeline = joblib.load(os.path.join(run_dir, "model.joblib"))
    features_cfg = cfg.get("features", {})

    df = load_parquet(cfg.get("data", {}).get("parquet_paths", []))
    annotations = load_annotations(cfg.get("data", {}).get("annotations_path", ""),
                                   label_columns=cfg.get("data", {}).get("annotation_label_columns",
                                                                        ("human_label", "human_provider")))
    df = merge_annotations(df, annotations, label_column=cfg.get("label", {}).get("column", "chosen_model"),
                           weight=float(cfg.get("data", {}).get("annotation_label_weight", 2.0)))
    df = clean_data(df, cfg.get("data", {}), features_cfg=features_cfg)
    sp = cfg.get("split", {})
    splits = split_dataset(df, label_column=LABEL_COL,
                           ratios=(sp.get("train", 0.7), sp.get("val", 0.15), sp.get("test", 0.15)),
                           random_state=int(sp.get("random_state", 42)))

    train, test = splits.train, splits.test
    train_hashes = set(train["content_hash"].dropna().astype(str))
    test_hashes = test["content_hash"].dropna().astype(str)

    seen_mask = test_hashes.isin(train_hashes).to_numpy()
    X_test = select_features(test, features_cfg)
    y_test = test[LABEL_COL].astype(str)
    pred = pipeline.predict(X_test)

    def acc(mask: np.ndarray) -> str:
        n = int(mask.sum())
        if n == 0:
            return "n=0"
        return f"{float((pred[mask] == y_test.to_numpy()[mask]).mean()):.4f} (n={n})"

    print(f"test total            : {len(test)}")
    print(f"accuracy seen-hash    : {acc(seen_mask)}   <- 泄漏区（同内容在训练集出现过）")
    print(f"accuracy unseen-hash  : {acc(~seen_mask)}   <- 诚实泛化区（新内容）")
    print(f"accuracy overall      : {float((pred == y_test.to_numpy()).mean()):.4f} (n={len(test)})")
    uniq_train = len(train_hashes)
    uniq_test = len(set(test_hashes))
    print(f"unique content_hash   : train={uniq_train} test={uniq_test}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
