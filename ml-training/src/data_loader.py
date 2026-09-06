# src/data_loader.py — P2.4 阶段1 数据加载
#
# 职责:
#   1. 加载P2.2导出的Parquet训练数据（schema与exporter/parquet_schema.go对齐）
#   2. 合并P2.1人工标注（training_human_annotations / annotator CSV），优先使用human_label
#   3. 数据清洗：missing标签、重复request_id、异常confidence、低频标签
#   4. 划分训练/验证/测试集（70/15/15，stratify=y）
#
# 隐私: 只接触结构化特征字段，不接触prompt/messages/response（与Go导出端契约一致）。

from __future__ import annotations

import glob
import os
from dataclasses import dataclass, field
from typing import Any, Optional, Sequence

import numpy as np
import pandas as pd
import yaml

# ---------------------------------------------------------------------------
# schema v1 常量（与 exporter/parquet_schema.go FieldNames() 一一对应）
# ---------------------------------------------------------------------------

#: Parquet中的全部25个字段
SCHEMA_V1_COLUMNS: tuple[str, ...] = (
    "request_id", "timestamp", "task_type", "profile", "classifier",
    "confidence",
    "detected_language", "prompt_length_bucket", "context_length_bucket",
    "turn_count_bucket", "has_code_indicator", "has_math_indicator",
    "has_table_indicator", "has_multimedia_indicator", "intent_category",
    "domain_hint", "complexity_bucket", "latency_sensitive", "cost_sensitive",
    "feature_version", "content_hash",
    "chosen_model", "success", "latency_ms", "reward",
)

#: 隐私合规：导出数据中禁止出现的字段（与Go端ProhibitedFields()一致）
PROHIBITED_FIELDS: tuple[str, ...] = (
    "prompt", "messages", "response", "summary", "keywords",
    "context", "user_id", "api_key", "ip_address",
)

#: 缺失类别特征的填充值
MISSING_CATEGORY = "__missing__"

# 合并后新增的辅助列（非schema字段）
LABEL_COL = "label"                    # 最终训练标签（human_label优先）
IS_ANNOTATED_COL = "is_annotated"      # 该行是否被人工标注覆盖
WEIGHT_COL = "sample_weight"           # 样本权重（人工标注 x weight）
RULE_CHOICE_COL = "rule_engine_choice" # 覆盖前的自动选择（用于规则引擎基线评估）


class DataLoadError(ValueError):
    """训练数据不满足契约（缺列/隐私违规/标签异常）时抛出。"""


# ---------------------------------------------------------------------------
# 配置加载
# ---------------------------------------------------------------------------

def load_config(path: str) -> dict[str, Any]:
    """读取config.yaml。相对路径以配置文件所在目录为基准解析。"""
    with open(path, "r", encoding="utf-8") as fh:
        cfg = yaml.safe_load(fh)
    if not isinstance(cfg, dict):
        raise DataLoadError(f"config file {path} does not contain a mapping")
    base = os.path.dirname(os.path.abspath(path))

    data_cfg = cfg.get("data", {})
    if isinstance(data_cfg, dict):
        paths = data_cfg.get("parquet_paths") or []
        if isinstance(paths, str):
            paths = [paths]
        resolved: list[str] = []
        for p in paths:
            if os.path.isabs(p):
                resolved.append(p)
            else:
                resolved.append(os.path.normpath(os.path.join(base, p)))
        if resolved:
            data_cfg["parquet_paths"] = resolved
        ann = data_cfg.get("annotations_path")
        if ann and not os.path.isabs(ann):
            data_cfg["annotations_path"] = os.path.normpath(os.path.join(base, ann))
    return cfg


# ---------------------------------------------------------------------------
# Parquet加载
# ---------------------------------------------------------------------------

def _expand_paths(parquet_paths: str | Sequence[str]) -> list[str]:
    if isinstance(parquet_paths, (str, os.PathLike)):
        parquet_paths = [str(parquet_paths)]
    files: list[str] = []
    for path in parquet_paths:
        if os.path.isdir(path):
            files.extend(sorted(glob.glob(os.path.join(path, "**", "*.parquet"), recursive=True)))
        else:
            matched = sorted(glob.glob(path))
            files.extend(matched if matched else [path])
    if not files:
        raise DataLoadError(f"no parquet files found for: {list(parquet_paths)}")
    missing = [f for f in files if not os.path.exists(f)]
    if missing:
        raise DataLoadError(f"parquet files not found: {missing}")
    return files


def load_parquet(parquet_paths: str | Sequence[str]) -> pd.DataFrame:
    """加载一个或多个Parquet文件并做schema/隐私校验。

    支持目录（递归*.parquet）、glob模式、显式文件路径或其列表。
    """
    files = _expand_paths(parquet_paths)
    frames = [pd.read_parquet(f) for f in files]
    df = pd.concat(frames, ignore_index=True) if len(frames) > 1 else frames[0]

    prohibited = [c for c in PROHIBITED_FIELDS if c in df.columns]
    if prohibited:
        raise DataLoadError(f"privacy violation: prohibited columns present: {prohibited}")

    missing_cols = [c for c in SCHEMA_V1_COLUMNS if c not in df.columns]
    if missing_cols:
        raise DataLoadError(
            f"parquet missing required schema v1 columns: {missing_cols} "
            f"(expected schema from exporter/parquet_schema.go)"
        )
    return df


# ---------------------------------------------------------------------------
# 人工标注加载与合并
# ---------------------------------------------------------------------------

def load_annotations(
    path: str,
    label_columns: Sequence[str] = ("human_label", "human_provider"),
) -> pd.DataFrame:
    """加载人工标注文件（CSV或Parquet）。

    兼容两种来源:
      - training_human_annotations 表导出（列: request_id, human_label, auto_label, ...）
      - llm-gw-annotator CSV（列: request_id, human_provider, auto_provider, ...）

    返回统一为 [request_id, human_label, auto_label(可空)] 的DataFrame，
    同一request_id多次标注时保留最后一条。
    """
    if not path:
        return pd.DataFrame(columns=["request_id", "human_label"])
    if not os.path.exists(path):
        raise DataLoadError(f"annotations file not found: {path}")

    if path.endswith(".parquet"):
        ann = pd.read_parquet(path)
    else:
        ann = pd.read_csv(path, dtype=str, keep_default_na=False, na_values=[""])

    if "request_id" not in ann.columns:
        raise DataLoadError(f"annotations missing request_id column: {list(ann.columns)}")

    label_col = next((c for c in label_columns if c in ann.columns), None)
    if label_col is None:
        raise DataLoadError(
            f"annotations missing label column (looked for {list(label_columns)}); "
            f"got {list(ann.columns)}"
        )

    auto_col = next((c for c in ("auto_label", "auto_provider") if c in ann.columns), None)

    out = pd.DataFrame({
        "request_id": ann["request_id"].astype(str),
        "human_label": ann[label_col].astype(str).str.strip(),
    })
    out["auto_label"] = ann[auto_col].astype(str) if auto_col else None
    # 标注顺序字段（存在则用于去重时保留最新），否则保留文件顺序
    if "annotated_at" in ann.columns:
        out["_order"] = pd.to_datetime(ann["annotated_at"], errors="coerce")
    else:
        out["_order"] = np.arange(len(out))

    out = out[out["human_label"].notna() & (out["human_label"] != "")]
    out = out.sort_values("_order").drop_duplicates("request_id", keep="last")
    return out.drop(columns="_order").reset_index(drop=True)


def merge_annotations(
    df: pd.DataFrame,
    annotations: pd.DataFrame,
    label_column: str = "chosen_model",
    weight: float = 2.0,
    require_known_label: bool = False,
) -> pd.DataFrame:
    """将人工标注合并到训练数据：human_label优先，未标注行沿用自动标签。

    新增列:
      label              最终训练标签
      rule_engine_choice 覆盖前的自动选择（评估规则引擎基线用）
      is_annotated       是否被人工标注覆盖
      sample_weight      1.0（自动） / weight（人工标注）

    require_known_label=True 时，human_label不在自动标签词表内的覆盖会被丢弃
    （防止provider/模型两级标签空间混用污染词表），并在返回列
    annotation_stats 中报告。
    """
    out = df.copy()
    out[RULE_CHOICE_COL] = out[label_column]
    out[LABEL_COL] = out[label_column]
    out[IS_ANNOTATED_COL] = False

    stats = {"annotation_rows": int(len(annotations)), "overrides_applied": 0,
             "overrides_dropped_unknown_label": 0, "matched_to_data": 0}
    if annotations is None or len(annotations) == 0:
        out[WEIGHT_COL] = 1.0
        out.attrs["annotation_stats"] = stats
        return out

    ann = annotations.copy()
    if require_known_label:
        known = set(out[label_column].astype(str).unique())
        unknown = ~ann["human_label"].isin(known)
        stats["overrides_dropped_unknown_label"] = int(unknown.sum())
        ann = ann[~unknown]
    if len(ann) == 0:
        out[WEIGHT_COL] = 1.0
        out.attrs["annotation_stats"] = stats
        return out

    ann = ann.drop_duplicates("request_id", keep="last")
    label_map = dict(zip(ann["request_id"], ann["human_label"]))
    mask = out["request_id"].astype(str).isin(label_map.keys())
    stats["matched_to_data"] = int(mask.sum())

    out.loc[mask, LABEL_COL] = out.loc[mask, "request_id"].astype(str).map(label_map)
    out.loc[mask, IS_ANNOTATED_COL] = True

    weights = np.ones(len(out), dtype=np.float64)
    weights[mask.to_numpy()] = float(weight)
    out[WEIGHT_COL] = weights
    stats["overrides_applied"] = int(mask.sum())
    out.attrs["annotation_stats"] = stats
    return out


# ---------------------------------------------------------------------------
# 特征规范化（训练/推理共用的特征准备契约，见 docs/ml/p2.4-training-pipeline.md）
# ---------------------------------------------------------------------------

def normalize_features(df: pd.DataFrame, features_cfg: dict[str, Any]) -> pd.DataFrame:
    """把特征列规范为Pipeline/ONNX期望的稳定dtype。

    契约（Go推理端必须实现同样的语义）:
      - 类别列: null → "__missing__"，统一为str
      - 布尔列: True→1, False→0, null→-1，统一为int8
      - 数值列: 统一为float64（NaN保留，由Pipeline中值填充）
    """
    out = df.copy()
    cat_cols, bool_cols, num_cols = features_from_config(features_cfg)

    # 只规范化DataFrame中实际存在的列（列存在性已由load_parquet按schema校验）
    for col in (c for c in cat_cols if c in out.columns):
        s = out[col]
        out[col] = s.astype("object").where(s.notna(), MISSING_CATEGORY).astype(str)

    for col in (c for c in bool_cols if c in out.columns):
        s = out[col]
        if s.dtype == object or str(s.dtype) == "boolean":
            # Parquet OPTIONAL BOOLEAN读入为object(含None)或nullable boolean
            out[col] = s.astype("boolean").astype("Int8").fillna(-1).astype("int8")
        elif pd.api.types.is_bool_dtype(s) or pd.api.types.is_integer_dtype(s):
            out[col] = s.astype("int8")
        else:
            # float等dtype：NaN无法直接转boolean
            out[col] = np.where(s.isna(), -1, s.fillna(0)).astype("int8")

    for col in (c for c in num_cols if c in out.columns):
        out[col] = pd.to_numeric(out[col], errors="coerce").astype("float64")

    return out


# ---------------------------------------------------------------------------
# 清洗
# ---------------------------------------------------------------------------

def clean_data(df: pd.DataFrame, cfg: dict[str, Any],
               features_cfg: dict[str, Any] | None = None) -> pd.DataFrame:
    """清洗训练数据：标签缺失、重复、异常confidence、低频标签、outcome过滤。

    features_cfg提供时，最后一步会把特征列规范为稳定dtype
    （normalize_features），使Pipeline输入不依赖Parquet读取细节。
    """
    out = df.copy()

    # 1) 标签必须非空
    label_col = LABEL_COL if LABEL_COL in out.columns else "chosen_model"
    before = len(out)
    out = out[out[label_col].notna()]
    out = out[out[label_col].astype(str).str.strip() != ""]
    dropped_label = before - len(out)

    # 2) 重复request_id（去重用content_hash在导出端已做，这里防御性去重）
    dropped_dup = 0
    if cfg.get("drop_duplicate_request_ids", True) and "request_id" in out.columns:
        before = len(out)
        out = out.drop_duplicates(subset=["request_id"], keep="first")
        dropped_dup = before - len(out)

    # 3) confidence异常值（越界行丢弃，缺失保留交由Pipeline中值填充）
    dropped_conf = 0
    lo, hi = float(cfg.get("min_confidence", 0.0)), float(cfg.get("max_confidence", 1.0))
    if "confidence" in out.columns:
        before = len(out)
        conf = pd.to_numeric(out["confidence"], errors="coerce")
        keep = conf.isna() | conf.between(lo, hi)
        out = out[keep]
        dropped_conf = before - len(out)

    # 4) outcome过滤（success_only: 只模仿成功的选择）
    dropped_outcome = 0
    mode = (cfg.get("outcome_filter") or "all").lower()
    if mode == "success_only" and "success" in out.columns:
        before = len(out)
        out = out[out["success"].fillna(False).astype(bool)]
        dropped_outcome = before - len(out)

    # 5) 低频标签整类丢弃（否则分层切分无法保证每个split含各类）
    dropped_rare = 0
    min_freq = int(cfg.get("min_label_frequency", 10))
    if min_freq > 1:
        before = len(out)
        counts = out[label_col].value_counts()
        keep_labels = counts[counts >= min_freq].index
        out = out[out[label_col].isin(keep_labels)]
        dropped_rare = before - len(out)

    # 6) 特征dtype规范化（见normalize_features的契约说明）
    if features_cfg is not None:
        out = normalize_features(out, features_cfg)

    out.attrs["cleaning_stats"] = {
        "rows_in": int(len(df)), "rows_out": int(len(out)),
        "dropped_missing_or_empty_label": int(dropped_label),
        "dropped_duplicate_request_id": int(dropped_dup),
        "dropped_out_of_range_confidence": int(dropped_conf),
        "dropped_by_outcome_filter": int(dropped_outcome),
        "dropped_rare_labels": int(dropped_rare),
    }
    return out


# ---------------------------------------------------------------------------
# 划分
# ---------------------------------------------------------------------------

@dataclass
class Splits:
    """训练/验证/测试三份切分。"""
    train: pd.DataFrame
    val: pd.DataFrame
    test: pd.DataFrame
    label_column: str = LABEL_COL
    meta: dict[str, Any] = field(default_factory=dict)

    @property
    def frames(self) -> tuple[pd.DataFrame, pd.DataFrame, pd.DataFrame]:
        return self.train, self.val, self.test


def split_dataset(
    df: pd.DataFrame,
    label_column: str = LABEL_COL,
    ratios: Sequence[float] = (0.70, 0.15, 0.15),
    random_state: int = 42,
) -> Splits:
    """按70/15/15分层划分（两步train_test_split，标签分布逐split校验）。"""
    if len(df) == 0:
        raise DataLoadError("cannot split an empty dataset")
    train_r, val_r, test_r = (float(r) for r in ratios)
    total = train_r + val_r + test_r
    if abs(total - 1.0) > 1e-6:
        raise DataLoadError(f"split ratios must sum to 1.0, got {ratios}")
    if min(train_r, val_r, test_r) <= 0:
        raise DataLoadError(f"split ratios must be positive, got {ratios}")

    labels = df[label_column].astype(str)
    per_label = labels.value_counts()
    # 分层切分要求每类在最小split中至少1条
    too_rare = per_label[per_label < 3]
    if len(too_rare) > 0:
        raise DataLoadError(
            f"labels too rare for stratified 3-way split (raise data.min_label_frequency): "
            f"{too_rare.to_dict()}"
        )

    from sklearn.model_selection import train_test_split

    idx = np.arange(len(df))
    test_frac = test_r / total
    train_idx, test_idx = train_test_split(
        idx, test_size=test_frac, random_state=random_state, stratify=labels
    )
    remaining_val_frac = val_r / (train_r + val_r)
    sub_labels = labels.iloc[train_idx]
    train_idx, val_idx = train_test_split(
        train_idx, test_size=remaining_val_frac,
        random_state=random_state, stratify=sub_labels,
    )

    take = lambda i: df.iloc[np.sort(i)].reset_index(drop=True)  # noqa: E731
    splits = Splits(
        train=take(train_idx), val=take(val_idx), test=take(test_idx),
        label_column=label_column,
        meta={"ratios": [train_r, val_r, test_r], "random_state": random_state},
    )
    splits.meta["label_distribution"] = {
        "train": splits.train[label_column].value_counts(normalize=True).round(4).to_dict(),
        "val": splits.val[label_column].value_counts(normalize=True).round(4).to_dict(),
        "test": splits.test[label_column].value_counts(normalize=True).round(4).to_dict(),
    }
    return splits


# ---------------------------------------------------------------------------
# 一站式入口
# ---------------------------------------------------------------------------

def load_training_data(cfg: dict[str, Any]) -> dict[str, Any]:
    """按config.yaml加载数据并完成合并/清洗/划分。

    返回 {"splits": Splits, "feature_columns": {...}, "stats": {...}}
    """
    data_cfg = cfg.get("data", {})
    label_column = cfg.get("label", {}).get("column", "chosen_model")
    features_cfg = cfg.get("features", {})
    split_cfg = cfg.get("split", {})

    df = load_parquet(data_cfg.get("parquet_paths", []))
    annotations = load_annotations(
        data_cfg.get("annotations_path") or "",
        label_columns=data_cfg.get("annotation_label_columns",
                                   ("human_label", "human_provider")),
    )
    df = merge_annotations(
        df, annotations,
        label_column=label_column,
        weight=float(data_cfg.get("annotation_label_weight", 2.0)),
        require_known_label=bool(data_cfg.get("annotation_require_known_label", False)),
    )
    df = clean_data(df, data_cfg, features_cfg=features_cfg)

    splits = split_dataset(
        df, label_column=LABEL_COL,
        ratios=split_cfg.get("ratios", (split_cfg.get("train", 0.7),
                                        split_cfg.get("val", 0.15),
                                        split_cfg.get("test", 0.15))),
        random_state=int(split_cfg.get("random_state", 42)),
    )

    return {
        "splits": splits,
        "feature_columns": features_cfg,
        "stats": {
            "rows_raw": int(len(df)),
            "annotation_stats": df.attrs.get("annotation_stats", {}),
            "cleaning_stats": df.attrs.get("cleaning_stats", {}),
            "n_labels": int(df[LABEL_COL].nunique()),
        },
    }


def features_from_config(features_cfg: dict[str, Any]) -> tuple[list[str], list[str], list[str]]:
    """返回(categorical, boolean, numeric)三组特征列名。"""
    return (
        list(features_cfg.get("categorical", [])),
        list(features_cfg.get("boolean", [])),
        list(features_cfg.get("numeric", [])),
    )


def privacy_check(df: pd.DataFrame) -> Optional[str]:
    """检查DataFrame是否包含禁止字段；违规时返回描述（供CLI与测试使用）。"""
    prohibited = [c for c in PROHIBITED_FIELDS if c in df.columns]
    return f"prohibited columns present: {prohibited}" if prohibited else None
