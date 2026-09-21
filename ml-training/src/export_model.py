# src/export_model.py — P2.4 模型导出
#
# 输出:
#   - model.joblib  — Python推理用（train.py总是产出）
#   - model.onnx    — 阶段4 Go集成用（ONNX Runtime；需安装 skl2onnx）
#
# 用法:
#   python -m src.export_model --run models/20260907-101500
#   python -m src.export_model --run models/20260907-101500 --skip-onnx
#
# ONNX输入契约（Go推理端必须遵守，见 docs/ml/p2.4-training-pipeline.md）:
#   - 每个特征列一个命名输入，形状[None, 1]
#       类别列  → string张量（缺失/未知传 "__missing__"）
#       布尔列  → int64张量（1/0，缺失传 -1）
#       数值列  → float张量（缺失传 NaN，图内中值填充）
#   - 输出: label(int64) + 各标签概率(float)
#   OrdinalEncoder/Imputer/Scaler/RandomForest全部编码进同一个ONNX图，
#   Go端不需要自己实现任何特征变换。

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any

import joblib
import numpy as np

from .data_loader import features_from_config
from .feature_pipeline import transformed_feature_names

JOBLIB_NAME = "model.joblib"
ONNX_NAME = "model.onnx"


def build_initial_types(features_cfg: dict[str, Any]) -> list[tuple[str, Any]]:
    """按config顺序构造每列的ONNX输入类型（类别string/布尔int64/数值float）。"""
    from skl2onnx.common.data_types import (
        FloatTensorType,
        Int64TensorType,
        StringTensorType,
    )

    cat_cols, bool_cols, num_cols = features_from_config(features_cfg)
    types: list[tuple[str, Any]] = []
    types += [(c, StringTensorType([None, 1])) for c in cat_cols]
    types += [(c, Int64TensorType([None, 1])) for c in bool_cols]
    types += [(c, FloatTensorType([None, 1])) for c in num_cols]
    return types


def export_onnx(run_dir: str, target_opset: int = 17) -> str:
    """将run目录中的Pipeline导出为ONNX，返回模型文件路径。"""
    try:
        from skl2onnx import to_onnx
    except ImportError as exc:
        raise RuntimeError(
            "ONNX export requires skl2onnx. Install with:\n"
            "  pip install skl2onnx onnxruntime\n"
            "(或使用 --skip-onnx 只导出joblib)"
        ) from exc

    model_path = os.path.join(run_dir, JOBLIB_NAME)
    if not os.path.exists(model_path):
        raise FileNotFoundError(f"model not found: {model_path}")
    pipeline = joblib.load(model_path)

    run_config = _config_from_run(run_dir)
    features_cfg = run_config.get("features", {})
    initial_types = build_initial_types(features_cfg)

    # zipmap=False: 概率输出为float32张量[N, n_classes]而非map，
    # 这是Go端onnxruntime_go可消费的形态（manifest.label_classes对齐列序）
    onnx_model = to_onnx(
        pipeline, initial_types=initial_types,
        target_opset={"": target_opset, "ai.onnx.ml": 3},
        options={id(pipeline): {"zipmap": False}},
    )

    out_path = os.path.join(run_dir, ONNX_NAME)
    with open(out_path, "wb") as fh:
        fh.write(onnx_model.SerializeToString())

    # manifest：Go推理端（P2.5 routingopt.MLSelector）的加载契约
    write_manifest(run_dir, pipeline, features_cfg, ONNX_NAME)

    # 可选验证：安装了onnxruntime则用哑输入跑一遍
    try:
        import onnxruntime as ort
        sess = ort.InferenceSession(out_path, providers=["CPUExecutionProvider"])
        feed = _dummy_feed(initial_types)
        sess.run(None, feed)
    except ImportError:
        pass  # onnxruntime未安装，跳过验证
    return out_path


MANIFEST_NAME = "manifest.json"


def write_manifest(run_dir: str, pipeline: Any, features_cfg: dict[str, Any],
                   model_file: str) -> str:
    """写manifest.json：ONNX输入顺序、标签类目、缺失哨兵值。

    Go端routingopt.MLSelector按此契约构造17个命名输入并解码标签，
    训练端与推理端通过该文件解耦（无需共享Python运行时）。
    """
    labels = [str(c) for c in pipeline.named_steps["model"].classes_]
    manifest = {
        "schema_version": "v1",
        "model_file": model_file,
        "label_classes": labels,
        "features": {
            "categorical": list(features_cfg.get("categorical", [])),
            "boolean": list(features_cfg.get("boolean", [])),
            "numeric": list(features_cfg.get("numeric", [])),
        },
        # 与 data_loader.normalize_features 的契约一致
        "missing_sentinels": {
            "categorical": "__missing__",
            "boolean_missing": -1,
            "numeric": "NaN",
        },
    }
    out_path = os.path.join(run_dir, MANIFEST_NAME)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, ensure_ascii=False, indent=2)
    return out_path


def _dummy_feed(initial_types: list[tuple[str, Any]]) -> dict[str, np.ndarray]:
    """为验证构造全"缺失"取值的输入（__missing__ / -1 / NaN）。"""
    from skl2onnx.common.data_types import (
        FloatTensorType,
        Int64TensorType,
        StringTensorType,
    )

    feed: dict[str, np.ndarray] = {}
    for name, ttype in initial_types:
        if isinstance(ttype, StringTensorType):
            feed[name] = np.array([["__missing__"]] * 2, dtype=object)
        elif isinstance(ttype, Int64TensorType):
            feed[name] = np.array([[-1]] * 2, dtype=np.int64)
        elif isinstance(ttype, FloatTensorType):
            feed[name] = np.full((2, 1), np.nan, dtype=np.float32)
    return feed


def _config_from_run(run_dir: str) -> dict[str, Any]:
    meta_path = os.path.join(run_dir, "metadata.json")
    if not os.path.exists(meta_path):
        raise FileNotFoundError(f"metadata not found: {meta_path}")
    with open(meta_path, "r", encoding="utf-8") as fh:
        return json.load(fh).get("config", {})


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m src.export_model",
        description="导出训练好的模型（joblib必导，ONNX可选）",
    )
    parser.add_argument("--run", required=True, help="训练run目录")
    parser.add_argument("--skip-onnx", action="store_true", help="跳过ONNX导出")
    parser.add_argument("--opset", type=int, default=17, help="ONNX target opset")
    args = parser.parse_args(argv)

    run_dir = args.run
    if not os.path.exists(os.path.join(run_dir, JOBLIB_NAME)):
        print(f"[error] {JOBLIB_NAME} not found in {run_dir}", file=sys.stderr)
        return 2

    print(f"joblib : {os.path.join(run_dir, JOBLIB_NAME)}")

    if args.skip_onnx:
        return 0

    try:
        out_path = export_onnx(run_dir, target_opset=args.opset)
    except RuntimeError as exc:
        print(f"[warn] {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001 — 转换器对个别opset组合可能不兼容
        print(f"[error] ONNX export failed: {exc}", file=sys.stderr)
        return 3

    size_mb = os.path.getsize(out_path) / (1024 * 1024)
    print(f"onnx   : {out_path} ({size_mb:.2f} MB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
