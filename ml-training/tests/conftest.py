# tests/conftest.py — 测试公共fixture
#
# 生成schema v1兼容的合成Parquet + 人工标注CSV（llm-gw-annotator格式），
# 并提供一个指向临时目录的完整config。

from __future__ import annotations

import copy
import sys
from pathlib import Path

import pytest

SRC = Path(__file__).resolve().parents[1] / "src"
if str(SRC) not in sys.path:
    sys.path.insert(0, str(SRC))

from src import synthetic  # noqa: E402

N_ROWS = 2400


@pytest.fixture(scope="session")
def synthetic_parquet(tmp_path_factory):
    """合成训练数据Parquet（schema v1全25列）。"""
    path = tmp_path_factory.mktemp("data") / "training_data.parquet"
    df = synthetic.generate_synthetic_training_data(n_rows=N_ROWS, seed=42)
    df.to_parquet(path, index=False)
    return path


@pytest.fixture(scope="session")
def annotations_csv(tmp_path_factory, synthetic_parquet):
    """人工标注CSV（llm-gw-annotator导出格式，human_provider列）。"""
    import pandas as pd

    path = tmp_path_factory.mktemp("ann") / "annotations.csv"
    df = pd.read_parquet(synthetic_parquet)
    ann = synthetic.generate_synthetic_annotations(df, n_annotated=120, seed=13)
    ann.to_csv(path, index=False)
    return path


@pytest.fixture()
def base_config(synthetic_parquet, annotations_csv, tmp_path):
    """完整训练config（dict形式），数据/输出指向临时目录。"""
    from src.data_loader import SCHEMA_V1_COLUMNS

    cat = ["task_type", "profile", "classifier", "detected_language",
           "prompt_length_bucket", "context_length_bucket", "turn_count_bucket",
           "intent_category", "domain_hint", "complexity_bucket"]
    boolean = ["has_code_indicator", "has_math_indicator", "has_table_indicator",
               "has_multimedia_indicator", "latency_sensitive", "cost_sensitive"]
    numeric = ["confidence"]
    assert len(cat) + len(boolean) + len(numeric) == 17

    return {
        "data": {
            "parquet_paths": [str(synthetic_parquet)],
            "annotations_path": str(annotations_csv),
            "annotation_label_weight": 2.0,
            "annotation_label_columns": ["human_label", "human_provider"],
            "drop_duplicate_request_ids": True,
            "min_confidence": 0.0,
            "max_confidence": 1.0,
            "min_label_frequency": 10,
            "outcome_filter": "all",
        },
        "label": {"column": "chosen_model"},
        "features": {"categorical": cat, "boolean": boolean, "numeric": numeric},
        "split": {"train": 0.70, "val": 0.15, "test": 0.15, "random_state": 42},
        "model": {
            "type": "random_forest", "n_estimators": 30, "max_depth": 20,
            "min_samples_split": 50, "min_samples_leaf": 1, "max_features": "sqrt",
            "class_weight": None, "random_state": 42, "n_jobs": 2,
        },
        "evaluation": {"rule_engine_accuracy": None, "random_baseline_seed": 42,
                       "top_k_features": 20},
        "export": {"format": "joblib", "onnx_target_opset": 17},
        "_meta": {"schema_columns": list(SCHEMA_V1_COLUMNS), "tmp_dir": str(tmp_path)},
    }


@pytest.fixture()
def config_without_annotations(base_config):
    """纯自动标注数据（无人工标注）的config。"""
    cfg = copy.deepcopy(base_config)
    cfg["data"]["annotations_path"] = ""
    return cfg
