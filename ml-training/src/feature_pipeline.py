# src/feature_pipeline.py — P2.4 阶段1 特征工程
#
# 职责:
#   - 类别特征编码（等价于按列LabelEncoder，但用OrdinalEncoder实现：
#     LabelEncoder设计上只用于标签y，不支持Pipeline/unknown处理；
#     OrdinalEncoder按列给出同样的整数编码且能处理未见类别）
#   - 数值特征归一化（中值填充 + StandardScaler）
#   - 全部封装进单个sklearn Pipeline（fit一次、可整体joblib、
#     且只含skl2onnx支持的标准组件，可无损导出ONNX供Go推理）
#
# 分工约定:
#   缺失值的具体填充（类别→"__missing__"、布尔→-1）在 data_loader.normalize_features
#   完成（训练/推理走同一Python加载路径）；Pipeline内的OrdinalEncoder再用
#   unknown_value=-1兜底未见类别。该约定同时是阶段4 Go推理端的特征准备契约，
#   详见 docs/ml/p2.4-training-pipeline.md。

from __future__ import annotations

from typing import Any

import pandas as pd
from sklearn.compose import ColumnTransformer
from sklearn.impute import SimpleImputer
from sklearn.pipeline import Pipeline
from sklearn.preprocessing import OrdinalEncoder, StandardScaler

from .data_loader import DataLoadError, features_from_config


def select_features(df: pd.DataFrame, features_cfg: dict[str, Any]) -> pd.DataFrame:
    """从DataFrame中按config顺序选出特征列（Pipeline的输入契约）。

    训练与推理都必须经过这一步：ONNX导出按"每列一个命名输入"生成，
    fit/predict时的列集合和顺序必须严格一致。
    """
    cat, boolean, numeric = features_from_config(features_cfg)
    cols = cat + boolean + numeric
    missing = [c for c in cols if c not in df.columns]
    if missing:
        raise DataLoadError(f"missing feature columns: {missing}")
    return df[cols].copy()


def build_feature_transformer(features_cfg: dict[str, Any]) -> ColumnTransformer:
    """构建ColumnTransformer：类别编码 + 布尔透传 + 数值归一化。

    只使用skl2onnx支持的标准组件（OrdinalEncoder/SimpleImputer/
    StandardScaler/passthrough），保证整个Pipeline可导出ONNX。
    """
    cat_cols, bool_cols, num_cols = features_from_config(features_cfg)

    return ColumnTransformer(
        transformers=[
            ("cat", OrdinalEncoder(
                handle_unknown="use_encoded_value",
                unknown_value=-1,          # 未见类别 → -1（推理端新类别的安全处理）
                encoded_missing_value=-1,  # 双保险：漏网缺失值 → -1
            ), cat_cols),
            ("bool", "passthrough", bool_cols),   # 已在loader规范为int8(-1=缺失)
            ("num", Pipeline([
                ("impute", SimpleImputer(strategy="median")),
                ("scale", StandardScaler()),
            ]), num_cols),
        ],
        remainder="drop",
        verbose_feature_names_out=False,
    )


def build_pipeline(features_cfg: dict[str, Any], model: Any) -> Pipeline:
    """特征变换 + 模型的完整Pipeline。"""
    return Pipeline([
        ("features", build_feature_transformer(features_cfg)),
        ("model", model),
    ])


def transformed_feature_names(features_cfg: dict[str, Any]) -> list[str]:
    """变换后的特征名（与ColumnTransformer输出列一一对应，用于特征重要性）。"""
    cat, boolean, num = features_from_config(features_cfg)
    return list(cat) + list(boolean) + list(num)
