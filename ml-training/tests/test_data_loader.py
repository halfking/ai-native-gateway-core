# tests/test_data_loader.py — 数据加载单元测试
#
# 覆盖: schema校验、隐私校验、标注合并（human_label优先/x2权重）、
#       数据清洗、70/15/15分层切分、特征dtype规范化。

from __future__ import annotations

import numpy as np
import pandas as pd
import pytest

from src.data_loader import (
    IS_ANNOTATED_COL,
    LABEL_COL,
    MISSING_CATEGORY,
    RULE_CHOICE_COL,
    SCHEMA_V1_COLUMNS,
    WEIGHT_COL,
    DataLoadError,
    clean_data,
    load_annotations,
    load_parquet,
    load_training_data,
    merge_annotations,
    normalize_features,
    privacy_check,
    split_dataset,
)


# ---------------------------------------------------------------------------
# Parquet加载
# ---------------------------------------------------------------------------

def test_load_parquet_reads_all_schema_columns(synthetic_parquet):
    df = load_parquet(str(synthetic_parquet))
    for col in SCHEMA_V1_COLUMNS:
        assert col in df.columns
    assert len(df) == 2400


def test_load_parquet_rejects_missing_columns(tmp_path):
    path = tmp_path / "broken.parquet"
    pd.DataFrame({"request_id": ["a"], "chosen_model": ["gpt-4"]}).to_parquet(path)
    with pytest.raises(DataLoadError, match="missing required schema"):
        load_parquet(str(path))


def test_load_parquet_rejects_privacy_fields(tmp_path):
    path = tmp_path / "leak.parquet"
    df = pd.DataFrame(0.0, index=range(3), columns=list(SCHEMA_V1_COLUMNS))
    df["prompt"] = "leak"
    df.to_parquet(path)
    with pytest.raises(DataLoadError, match="privacy violation"):
        load_parquet(str(path))


def test_privacy_check_helper():
    assert privacy_check(pd.DataFrame(columns=["prompt"])) is not None
    assert privacy_check(pd.DataFrame(columns=["task_type"])) is None


# ---------------------------------------------------------------------------
# 标注加载与合并
# ---------------------------------------------------------------------------

def test_load_annotations_annotator_csv_format(annotations_csv):
    ann = load_annotations(str(annotations_csv))
    assert set(ann.columns) == {"request_id", "human_label", "auto_label"}
    assert ann["human_label"].str.len().gt(0).all()
    # request_id唯一（多次标注保留最新）
    assert ann["request_id"].is_unique


def test_merge_annotations_overrides_label_and_weight(synthetic_parquet, annotations_csv):
    df = load_parquet(str(synthetic_parquet))
    ann = load_annotations(str(annotations_csv))
    merged = merge_annotations(df, ann, label_column="chosen_model", weight=2.0)

    ann_map = dict(zip(ann["request_id"], ann["human_label"]))
    annotated = merged[merged[IS_ANNOTATED_COL]]
    assert len(annotated) == len(ann_map)
    assert (annotated[LABEL_COL] == annotated["request_id"].map(ann_map)).all()
    # 覆盖行权重=2，其余=1
    assert (annotated[WEIGHT_COL] == 2.0).all()
    assert (merged.loc[~merged[IS_ANNOTATED_COL], WEIGHT_COL] == 1.0).all()
    # rule_engine_choice保留覆盖前的自动选择
    assert (annotated[RULE_CHOICE_COL] == annotated["chosen_model"]).all()
    # 未覆盖行的标签与自动标签一致
    auto_rows = merged.loc[~merged[IS_ANNOTATED_COL], LABEL_COL]
    assert (auto_rows == merged.loc[~merged[IS_ANNOTATED_COL], "chosen_model"]).all()


def test_merge_annotations_require_known_label_drops_unknown(synthetic_parquet):
    df = load_parquet(str(synthetic_parquet))
    ann = pd.DataFrame({
        "request_id": [df["request_id"].iloc[0], df["request_id"].iloc[1]],
        "human_label": ["gpt-4", "nonexistent_provider"],
    })
    merged = merge_annotations(df, ann, label_column="chosen_model",
                               require_known_label=True)
    stats = merged.attrs["annotation_stats"]
    assert stats["overrides_dropped_unknown_label"] == 1
    assert stats["overrides_applied"] == 1


def test_merge_annotations_empty_keeps_autolabels(config_without_annotations,
                                                  synthetic_parquet):
    df = load_parquet(str(synthetic_parquet))
    merged = merge_annotations(df, pd.DataFrame(columns=["request_id", "human_label"]),
                               label_column="chosen_model")
    assert (merged[LABEL_COL] == merged["chosen_model"]).all()
    assert (merged[WEIGHT_COL] == 1.0).all()


# ---------------------------------------------------------------------------
# 清洗
# ---------------------------------------------------------------------------

def test_clean_data_drops_duplicates_and_bad_confidence(synthetic_parquet):
    df = load_parquet(str(synthetic_parquet))
    dirty = pd.concat([
        df, df.head(5),  # 5条重复request_id
    ], ignore_index=True)
    dirty.loc[0, "confidence"] = 5.0     # 越界
    dirty.loc[10, "chosen_model"] = ""   # 空标签（非重复行，避免与去重计数耦合）

    cleaned = clean_data(dirty, {"drop_duplicate_request_ids": True,
                                 "min_confidence": 0.0, "max_confidence": 1.0,
                                 "min_label_frequency": 10})
    stats = cleaned.attrs["cleaning_stats"]
    assert stats["dropped_duplicate_request_id"] == 5
    assert stats["dropped_out_of_range_confidence"] == 1
    assert stats["dropped_missing_or_empty_label"] >= 1
    assert cleaned["request_id"].is_unique
    assert cleaned["confidence"].fillna(0).between(0, 1).all()


def test_clean_data_drops_rare_labels(synthetic_parquet):
    df = load_parquet(str(synthetic_parquet))
    df.loc[df.index[:3], "chosen_model"] = "rare-model"  # 仅3条，低于阈值10
    cleaned = clean_data(df, {"min_label_frequency": 10})
    assert "rare-model" not in cleaned["chosen_model"].unique()
    assert cleaned.attrs["cleaning_stats"]["dropped_rare_labels"] == 3


def test_clean_data_success_filter(synthetic_parquet):
    df = load_parquet(str(synthetic_parquet))
    cleaned = clean_data(df, {"min_label_frequency": 1, "outcome_filter": "success_only"})
    assert cleaned["success"].fillna(False).astype(bool).all()


# ---------------------------------------------------------------------------
# 切分
# ---------------------------------------------------------------------------

def test_split_dataset_proportions_and_stratification():
    rng = np.random.default_rng(0)
    n_per_label = 300
    df = pd.DataFrame({
        "request_id": [f"r{i}" for i in range(n_per_label * 3)],
        "label": (["a"] * n_per_label + ["b"] * n_per_label + ["c"] * n_per_label),
        "x": rng.normal(size=n_per_label * 3),
    })
    splits = split_dataset(df, label_column="label",
                           ratios=(0.7, 0.15, 0.15), random_state=42)
    n = len(df)
    assert len(splits.train) == round(n * 0.7)
    assert len(splits.val) == round(n * 0.15)
    assert len(splits.test) == n - len(splits.train) - len(splits.val)

    # 分层：每个split内标签占比与整体一致（±2%）
    overall = df["label"].value_counts(normalize=True)
    for frame in splits.frames:
        part = frame["label"].value_counts(normalize=True)
        for label, share in overall.items():
            assert abs(part[label] - share) < 0.02

    # 无泄漏：request_id不跨split
    ids = set(df["request_id"])
    seen = set()
    for frame in splits.frames:
        assert len(seen & set(frame["request_id"])) == 0
        seen |= set(frame["request_id"])
    assert seen == ids


def test_split_dataset_rejects_bad_ratios():
    df = pd.DataFrame({"request_id": ["a"], "label": ["x"]})
    with pytest.raises(DataLoadError, match="sum to 1.0"):
        split_dataset(df, ratios=(0.5, 0.5, 0.5))


# ---------------------------------------------------------------------------
# 特征规范化
# ---------------------------------------------------------------------------

def test_normalize_features_contract(base_config):
    features_cfg = base_config["features"]
    df = pd.DataFrame({
        "detected_language": [None, "zh", "en"],
        "has_code_indicator": [None, True, False],
        "confidence": [None, 0.5, 1.5],
    })
    norm = normalize_features(df, features_cfg)
    assert norm["detected_language"].tolist() == [MISSING_CATEGORY, "zh", "en"]
    assert norm["has_code_indicator"].tolist() == [-1, 1, 0]
    assert norm["has_code_indicator"].dtype == "int8"
    assert norm["confidence"].isna().tolist() == [True, False, False]


# ---------------------------------------------------------------------------
# 一站式入口
# ---------------------------------------------------------------------------

def test_load_training_data_end_to_end(base_config):
    out = load_training_data(base_config)
    splits = out["splits"]
    total = sum(len(f) for f in splits.frames)
    assert total > 2000
    assert abs(len(splits.train) / total - 0.70) < 0.01
    assert LABEL_COL in splits.train.columns
    assert WEIGHT_COL in splits.train.columns
    # 人工标注被合并进来
    stats = out["stats"]["annotation_stats"]
    assert stats["overrides_applied"] > 0
    # 特征已规范化
    assert splits.train["has_code_indicator"].dtype == "int8"
    assert (splits.train["detected_language"] != "").all()
