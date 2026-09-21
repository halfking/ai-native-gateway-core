#!/usr/bin/env python
# ml-training/scripts/make_go_fixture.py — 生成Go端推理测试fixture
#
# 训练一个极小的RandomForest并导出ONNX + manifest.json 到
# routingopt/testdata/ml_fixture/，供 routingopt/ml_selector_test.go 使用。
#
# 用法:
#   cd ml-training && .venv/bin/python scripts/make_go_fixture.py \
#       --out ../routingopt/testdata/ml_fixture
#
# fixture要求: 模型小（<300KB）、可复现（固定seed）、manifest与ONNX一致。

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from sklearn.ensemble import RandomForestClassifier  # noqa: E402

from src import synthetic  # noqa: E402
from src.data_loader import clean_data, load_config, merge_annotations  # noqa: E402
from src.data_loader import load_annotations, split_dataset  # noqa: E402
from src.export_model import build_initial_types, write_manifest  # noqa: E402
from src.feature_pipeline import build_pipeline, select_features  # noqa: E402

CONFIG = Path(__file__).resolve().parents[1] / "config.yaml"


def main() -> int:
    parser = argparse.ArgumentParser(description="生成Go推理测试fixture")
    parser.add_argument("--out", required=True, help="输出目录（相对或绝对路径）")
    parser.add_argument("--rows", type=int, default=3000, help="合成数据行数")
    parser.add_argument("--trees", type=int, default=6, help="RF树数量")
    args = parser.parse_args()

    cfg = load_config(str(CONFIG))
    features_cfg = cfg["features"]

    # 始终用当前代码现场生成（固定seed保证可复现）。
    # 不读取data/下的旧parquet——那可能是旧词汇表版本生成的，
    # 会导致fixture的ONNX编码器词表与运行时契约脱节。
    df = synthetic.generate_synthetic_training_data(n_rows=args.rows, seed=42)
    df = clean_data(df, cfg.get("data", {}), features_cfg=features_cfg)
    df = merge_annotations(df, load_annotations(""), label_column="chosen_model")

    splits = split_dataset(df, ratios=(0.8, 0.1, 0.1), random_state=42)
    model = RandomForestClassifier(
        n_estimators=args.trees, max_depth=6, min_samples_split=10,
        random_state=42, n_jobs=1,
    )
    pipeline = build_pipeline(features_cfg, model)
    X = select_features(splits.train, features_cfg)
    pipeline.fit(X, splits.train["label"].astype(str))

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    # 直接在目标目录内组装run布局（joblib仅为manifest生成所需）
    import joblib
    import tempfile

    with tempfile.TemporaryDirectory() as tmp:
        joblib.dump(pipeline, os.path.join(tmp, "model.joblib"))
        from skl2onnx import to_onnx

        onnx_model = to_onnx(
            pipeline, initial_types=build_initial_types(features_cfg),
            target_opset={"": 17, "ai.onnx.ml": 3},
            options={id(pipeline): {"zipmap": False}},
        )
        onnx_path = out_dir / "model.onnx"
        with open(onnx_path, "wb") as fh:
            fh.write(onnx_model.SerializeToString())
        manifest_path = write_manifest(str(out_dir), pipeline, features_cfg,
                                       onnx_path.name)

    # 验证：ORT可用时用哑输入跑一遍
    try:
        import onnxruntime as ort

        sess = ort.InferenceSession(str(onnx_path), providers=["CPUExecutionProvider"])
        from src.export_model import _dummy_feed

        sess.run(None, _dummy_feed(build_initial_types(features_cfg)))
    except ImportError:
        print("[warn] onnxruntime未安装，跳过fixture验证")

    size_kb = onnx_path.stat().st_size / 1024
    print(f"fixture written: {onnx_path} ({size_kb:.0f} KB), {manifest_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
