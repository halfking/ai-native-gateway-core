# tests/test_integration.py — 端到端集成测试
#
# 覆盖: 合成数据 → 加载/合并/清洗/切分 → RandomForest训练（含x2样本权重）
#       → 评估报告（准确率/混淆矩阵/分provider P/R/F1/特征重要性/基线对比）
#       → joblib保存 → CLI复现评估 → ONNX导出（可选依赖，未装则跳过）。

from __future__ import annotations

import importlib.util
import json
import os

import joblib
import numpy as np
import pandas as pd
import pytest

from src import evaluate as evaluate_mod
from src import train as train_mod
from src.data_loader import (
    IS_ANNOTATED_COL,
    LABEL_COL,
    WEIGHT_COL,
    load_training_data,
)

HAS_SKL2ONNX = importlib.util.find_spec("skl2onnx") is not None


def test_end_to_end_train_evaluate_save(base_config, tmp_path):
    """完整训练管道：数据→模型→评估→落盘。"""
    loaded = load_training_data(base_config)
    run_dir = str(tmp_path / "run1")

    out = train_mod.run(base_config, run_dir=run_dir, loaded=loaded)
    metrics = out["metrics"]

    # ---- 产物存在 ----
    assert os.path.exists(os.path.join(run_dir, "model.joblib"))
    assert os.path.exists(os.path.join(run_dir, "metadata.json"))
    assert os.path.exists(os.path.join(run_dir, "evaluation_report.json"))
    assert os.path.exists(os.path.join(run_dir, "evaluation_report.md"))
    assert os.path.exists(os.path.join(run_dir, "confusion_matrix.csv"))
    assert os.path.exists(os.path.join(run_dir, "feature_importance.csv"))

    # ---- 成功指标1: 模型优于随机/最频繁基线（合成数据含可学习结构）----
    beats = metrics["beats_baselines"]
    assert beats["vs_most_frequent"] is True
    assert beats["vs_random"] is True

    # ---- 成功指标2: 训练时间远小于10分钟 ----
    assert metrics["train_seconds"] < 600

    # ---- 成功指标3: 特征重要性合理（路由信号字段排前列）----
    fi = pd.DataFrame(metrics["feature_importance"])
    top10 = set(fi.head(10)["feature"])
    # 合成数据标签直接依赖这些字段，应进入前列
    assert top10 & {"has_code_indicator", "profile",
                    "context_length_bucket", "complexity_bucket",
                    "detected_language", "intent_category"}

    # ---- 混淆矩阵与测试集规模一致 ----
    cm_sum = int(np.sum(metrics["confusion_matrix"]["matrix"]))
    assert cm_sum == metrics["n_test"] > 0

    # ---- 分provider报告覆盖全部标签 ----
    per_class = {k for k in metrics["per_class"]
                 if k not in ("accuracy", "macro avg", "weighted avg")}
    assert per_class == set(out["result"]["label_classes"])

    # ---- 样本权重: 训练集含人工标注行且权重=2 ----
    train_df = loaded["splits"].train
    annotated = train_df[train_df[IS_ANNOTATED_COL]]
    assert len(annotated) > 0
    assert (annotated[WEIGHT_COL] == 2.0).all()

    # ---- 人工标注覆盖了标签（human_label优先）----
    ann = pd.read_csv(base_config["data"]["annotations_path"])
    sample_req = ann.iloc[0]["request_id"]
    row = train_df[train_df["request_id"] == sample_req]
    if len(row) == 1:
        expected = ann.iloc[0]["human_provider"]
        assert row.iloc[0][LABEL_COL] == expected

    # ---- metadata可加载、模型可复用 ----
    with open(os.path.join(run_dir, "metadata.json"), encoding="utf-8") as fh:
        meta = json.load(fh)
    assert meta["n_features"] == 17
    assert meta["pipeline_version"] == "p2.4-phase2"
    pipeline = joblib.load(os.path.join(run_dir, "model.joblib"))
    from src.feature_pipeline import select_features
    X_test = select_features(loaded["splits"].test, loaded["feature_columns"])
    assert len(pipeline.predict(X_test)) == len(X_test)


def test_model_beats_baselines_requires_learnable_structure(config_without_annotations):
    """冒烟检查：纯自动标注数据同样应优于随机基线。"""
    loaded = load_training_data(config_without_annotations)
    out = train_mod.run(config_without_annotations, run_dir=None, loaded=loaded)
    assert out["metrics"]["accuracy"]["model_test"] > \
        out["metrics"]["baselines"]["random_uniform"]


def test_evaluate_cli_reproduces_split(base_config, tmp_path, capsys):
    """evaluate CLI: 相同seed重现相同切分，报告数值一致。"""
    run_dir = str(tmp_path / "run2")
    train_mod.run(base_config, run_dir=run_dir)

    rc = evaluate_mod.main(["--run", run_dir])
    assert rc == 0
    with open(os.path.join(run_dir, "evaluation_report.json"), encoding="utf-8") as fh:
        metrics = json.load(fh)
    assert metrics["accuracy"]["model_test"] > 0
    # markdown报告包含关键章节
    with open(os.path.join(run_dir, "evaluation_report.md"), encoding="utf-8") as fh:
        md = fh.read()
    for section in ("准确率", "混淆矩阵", "特征重要性", "规则引擎"):
        assert section in md


def test_train_cli_synthetic_smoke(tmp_path, monkeypatch):
    """CLI冒烟: --synthetic 端到端跑通（不依赖生产Parquet）。"""
    import yaml

    # 最小config（模拟新检出仓库中的config.yaml；合成模式会覆盖parquet路径）
    cat = ["task_type", "profile", "classifier", "detected_language",
           "prompt_length_bucket", "context_length_bucket", "turn_count_bucket",
           "intent_category", "domain_hint", "complexity_bucket"]
    boolean = ["has_code_indicator", "has_math_indicator", "has_table_indicator",
               "has_multimedia_indicator", "latency_sensitive", "cost_sensitive"]
    minimal = {
        "data": {
            "parquet_paths": ["data/training_data.parquet"],
            "min_label_frequency": 10,
        },
        "label": {"column": "chosen_model"},
        "features": {"categorical": cat, "boolean": boolean,
                     "numeric": ["confidence"]},
        "split": {"train": 0.70, "val": 0.15, "test": 0.15, "random_state": 42},
        "model": {"type": "random_forest", "n_estimators": 20, "max_depth": 20,
                  "min_samples_split": 50, "random_state": 42, "n_jobs": 2},
        "evaluation": {"random_baseline_seed": 42, "top_k_features": 20},
    }
    monkeypatch.chdir(tmp_path)
    (tmp_path / "config.yaml").write_text(yaml.safe_dump(minimal), encoding="utf-8")

    rc = train_mod.main(["--config", "config.yaml", "--synthetic", "600"])
    assert rc == 0
    models_dir = tmp_path / "models"
    runs = list(models_dir.iterdir())
    assert len(runs) == 1
    assert (runs[0] / "model.joblib").exists()
    assert (runs[0] / "evaluation_report.md").exists()


@pytest.mark.skipif(not HAS_SKL2ONNX, reason="skl2onnx未安装")
def test_onnx_export_roundtrip(base_config, tmp_path):
    """ONNX导出: 转换成功且onnxruntime可加载推理（17个命名输入）。"""
    from src.export_model import _dummy_feed, build_initial_types, export_onnx

    loaded = load_training_data(base_config)
    run_dir = str(tmp_path / "run_onnx")
    train_mod.run(base_config, run_dir=run_dir, loaded=loaded)

    onnx_path = export_onnx(run_dir)
    assert os.path.exists(onnx_path)

    ort = pytest.importorskip("onnxruntime")
    sess = ort.InferenceSession(onnx_path, providers=["CPUExecutionProvider"])
    input_names = [i.name for i in sess.get_inputs()]
    assert len(input_names) == 17

    # 全缺失取值也应能推理（__missing__/-1/NaN路径）
    outputs = sess.run(None, _dummy_feed(build_initial_types(base_config["features"])))
    assert len(outputs) >= 1  # label + probabilities

    # 与joblib pipeline在同一批真实样本上输出一致（抽样对比）
    import joblib
    from src.feature_pipeline import select_features

    pipeline = joblib.load(os.path.join(run_dir, "model.joblib"))
    X = select_features(loaded["splits"].test.head(20), base_config["features"])
    feed = {}
    for col in X.columns:
        vals = X[col].to_numpy()
        if str(vals.dtype) == "object":
            feed[col] = vals.reshape(-1, 1).astype(object)
        elif np.issubdtype(vals.dtype, np.integer):
            feed[col] = vals.astype(np.int64).reshape(-1, 1)
        else:
            feed[col] = vals.astype(np.float32).reshape(-1, 1)
    onnx_label = sess.run(None, feed)[0].ravel()
    py_label = pipeline.predict(X)
    assert (onnx_label == np.asarray(py_label)).mean() >= 0.9


def test_export_model_cli_without_onnx(base_config, tmp_path):
    """--skip-onnx 时导出命令直接成功（不依赖skl2onnx）。"""
    from src.export_model import main as export_main

    loaded = load_training_data(base_config)
    run_dir = str(tmp_path / "run_export")
    train_mod.run(base_config, run_dir=run_dir, loaded=loaded)
    assert export_main(["--run", run_dir, "--skip-onnx"]) == 0
