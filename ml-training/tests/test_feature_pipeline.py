# tests/test_feature_pipeline.py — 特征工程单元测试
#
# 覆盖: 变换输出形状/无NaN、未见类别→-1、缺失数值中值填充+归一化、
#       Pipeline整体fit/predict、特征名与重要性列对齐、blob可持久化(joblib)。

from __future__ import annotations

import joblib
import numpy as np
import pandas as pd
import pytest
from sklearn.ensemble import RandomForestClassifier

from src.data_loader import LABEL_COL, normalize_features
from src.feature_pipeline import (
    build_pipeline,
    select_features,
    transformed_feature_names,
)


@pytest.fixture()
def small_splits(base_config):
    """规范化后的200行小数据集（已过normalize_features）。"""
    from src import synthetic
    from src.data_loader import load_parquet

    df = load_parquet(str(base_config["data"]["parquet_paths"][0])).head(200)
    # 提高低频阈值以保留全部标签
    df = df[df["chosen_model"].isin(df["chosen_model"].value_counts().head(3).index)]
    df = normalize_features(df, base_config["features"])
    return df, base_config["features"]


def test_transformed_feature_names_match_config(base_config):
    names = transformed_feature_names(base_config["features"])
    cfg = base_config["features"]
    assert names == (cfg["categorical"] + cfg["boolean"] + cfg["numeric"])
    assert len(names) == 17


def test_pipeline_transform_output_has_no_nan(small_splits):
    df, features_cfg = small_splits
    X = select_features(df, features_cfg)
    pipeline = build_pipeline(features_cfg, RandomForestClassifier(n_estimators=5))
    Xt = pipeline.named_steps["features"].fit_transform(X)
    assert Xt.shape[0] == len(X)
    assert Xt.shape[1] == len(transformed_feature_names(features_cfg))
    assert not np.isnan(Xt.astype("float64")).any()


def test_unknown_category_maps_to_minus_one(small_splits):
    df, features_cfg = small_splits
    X = select_features(df, features_cfg)
    pipeline = build_pipeline(features_cfg, RandomForestClassifier(n_estimators=5))
    pipeline.named_steps["features"].fit(X)

    novel = X.head(1).copy()
    novel["detected_language"] = "klingon"       # 训练中未见
    novel["confidence"] = 0.5
    Xt = pipeline.named_steps["features"].transform(novel)
    lang_idx = transformed_feature_names(features_cfg).index("detected_language")
    assert Xt[0, lang_idx] == -1


def test_numeric_imputation_and_scaling(small_splits):
    df, features_cfg = small_splits
    X = select_features(df, features_cfg)
    X.loc[X.index[:20], "confidence"] = np.nan   # 注入缺失
    transformer = build_pipeline(
        features_cfg, RandomForestClassifier(n_estimators=5)
    ).named_steps["features"]
    Xt = transformer.fit_transform(X)
    assert not np.isnan(Xt.astype("float64")).any()
    conf_idx = transformed_feature_names(features_cfg).index("confidence")
    col = Xt[:, conf_idx]
    # StandardScaler后均值≈0、标准差≈1（中值填充不改变这一统计性质的方向）
    assert abs(col.mean()) < 1e-6
    assert 0.1 < col.std() < 10.0


def test_full_pipeline_fit_predict_and_joblib_roundtrip(small_splits, tmp_path):
    df, features_cfg = small_splits
    X = select_features(df, features_cfg)
    y = df["chosen_model"].astype(str)

    pipeline = build_pipeline(features_cfg, RandomForestClassifier(
        n_estimators=10, random_state=42, n_jobs=2))
    pipeline.fit(X, y)
    preds = pipeline.predict(X)
    assert len(preds) == len(X)
    assert set(preds).issubset(set(y.unique()))

    # 序列化往返（推理端加载路径）
    path = tmp_path / "pipeline.joblib"
    joblib.dump(pipeline, path)
    loaded = joblib.load(path)
    np.testing.assert_array_equal(loaded.predict(X), preds)


def test_boolean_missing_encoded_as_minus_one(base_config):
    """null布尔特征规范为-1且与False(0)可区分。"""
    features_cfg = base_config["features"]
    df = pd.DataFrame({
        "has_code_indicator": [None, None, True],
        "latency_sensitive": [False, True, None],
    })
    norm = normalize_features(df, features_cfg)
    assert norm["has_code_indicator"].tolist() == [-1, -1, 1]
    assert norm["latency_sensitive"].tolist() == [0, 1, -1]
