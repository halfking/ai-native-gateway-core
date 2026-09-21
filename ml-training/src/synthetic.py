# src/synthetic.py — 合成训练数据生成（schema v1兼容）
#
# 用途:
#   - 无生产Parquet时的端到端冒烟测试（python -m src.train --synthetic 5000）
#   - 单元测试/集成测试的固定数据源
#
# 注意: 特征与标签之间注入了可学习的结构（不是纯随机），
#       使得"模型应优于随机/最频繁基线"这一命题在冒烟环境下可被验证。
#       字段与 exporter/parquet_schema.go 的25列一一对应。

from __future__ import annotations

import numpy as np
import pandas as pd

# 词汇表与Go运行时（写入auto_route_selections的值）严格对齐：
#   - autoroute/structured_features.go（buckets/enums）
#   - autoroute/profile.go（Profile枚举）
#   - autoroute/task_types*.go（TaskType枚举）
#   - classifier字段：v3_heuristic/llm/heuristic/default（Classification.Classifier）
# 这样合成fixture与真实训练数据共享同一ONNX编码器词表，避免unknown→-1退化。
LANGS = ["zh", "en", "ja", "ko", "mixed", "unknown", None]
LEN_BUCKETS = ["xs", "s", "m", "l", "xl", "xxl"]
CTX_BUCKETS = ["xs", "s", "m", "l", "xl"]
TURN_BUCKETS = ["single", "few", "many", "very_many"]
INTENTS = ["question", "instruction", "conversation", "analysis", "generation", None]
DOMAINS = ["general", "technical", "business", "academic", "creative", None]
COMPLEXITY = ["trivial", "simple", "moderate", "complex", "very_complex"]
TASKS = ["chat", "reasoning", "code", "agent", "creative", "long_context",
         "vision", "function_call"]
PROFILES = ["smart", "speed_first", "cost_first"]
CLASSIFIERS = ["v3_heuristic", "llm", "heuristic", "default"]
MODELS = ["gpt-4", "claude-3.5-sonnet", "glm-4.6", "deepseek-v3"]

BOOL_COLS = [
    "has_code_indicator", "has_math_indicator", "has_table_indicator",
    "has_multimedia_indicator", "latency_sensitive", "cost_sensitive",
]


def generate_synthetic_training_data(
    n_rows: int = 2000,
    seed: int = 42,
    null_rate: float = 0.08,
) -> pd.DataFrame:
    """生成n_rows条schema v1兼容的合成记录。

    标签规则（可学习结构，使用运行时词汇表）:
      - code/agent任务 + 表格类请求               → claude-3.5-sonnet
      - long_context任务 或 xl上下文 + analysis   → gpt-4
      - 中文 + trivial/simple复杂度               → glm-4.6
      - cost_first profile                        → deepseek-v3
      - 其余按启发式概率分布
    """
    rng = np.random.default_rng(seed)
    n = int(n_rows)

    df = pd.DataFrame({
        "request_id": [f"syn_{i:08d}" for i in range(n)],
        "timestamp": (1_750_000_000_000 + rng.integers(0, 86_400_000, n)),
        "task_type": rng.choice(TASKS, n,
                                p=[0.45, 0.10, 0.15, 0.05, 0.05, 0.10, 0.05, 0.05]),
        "profile": rng.choice(PROFILES, n, p=[0.6, 0.2, 0.2]),
        "classifier": rng.choice(CLASSIFIERS, n, p=[0.8, 0.1, 0.08, 0.02]),
        "confidence": np.round(rng.beta(5, 2, n), 3),
    })

    optional_str = {
        "detected_language": rng.choice(
            LANGS, n, p=[0.4, 0.3, 0.08, 0.07, 0.05, 0.05, 0.05]),
        "prompt_length_bucket": rng.choice(LEN_BUCKETS, n,
                                           p=[0.15, 0.25, 0.3, 0.15, 0.1, 0.05]),
        "context_length_bucket": rng.choice(CTX_BUCKETS, n,
                                            p=[0.3, 0.25, 0.2, 0.15, 0.1]),
        "turn_count_bucket": rng.choice(TURN_BUCKETS, n, p=[0.35, 0.35, 0.2, 0.1]),
        "intent_category": rng.choice(INTENTS, n,
                                      p=[0.35, 0.25, 0.15, 0.1, 0.07, 0.08]),
        "domain_hint": rng.choice(DOMAINS, n,
                                  p=[0.4, 0.25, 0.12, 0.08, 0.1, 0.05]),
        "complexity_bucket": rng.choice(COMPLEXITY, n,
                                        p=[0.3, 0.3, 0.25, 0.1, 0.05]),
    }
    for col, values in optional_str.items():
        # None值通过choice概率注入，模拟Parquet的OPTIONAL列
        df[col] = pd.Series(values, dtype="object")

    for col in BOOL_COLS:
        p = 0.2 if col not in ("latency_sensitive", "cost_sensitive") else 0.15
        arr = rng.random(n) < p
        s = pd.Series(arr, dtype="object")
        if null_rate > 0:
            mask = rng.random(n) < null_rate
            s[mask] = None
        df[col] = s

    df["feature_version"] = "v1"
    df["content_hash"] = [f"{rng.integers(0, 2**63):016x}" for _ in range(n)]

    # ---- 可学习标签结构（运行时词汇表）----
    labels = []
    for i in range(n):
        row = df.iloc[i]
        codey = bool(row["has_code_indicator"]) or bool(row["has_table_indicator"])
        long_ctx = row["context_length_bucket"] in ("l", "xl")
        zh = row["detected_language"] == "zh"
        simple = row["complexity_bucket"] in ("trivial", "simple")
        if codey and row["task_type"] in ("code", "agent"):
            labels.append("claude-3.5-sonnet")
        elif long_ctx and row["intent_category"] == "analysis":
            labels.append("gpt-4")
        elif zh and simple:
            labels.append("glm-4.6")
        elif row["profile"] == "cost_first":
            labels.append("deepseek-v3")
        else:
            labels.append(rng.choice(MODELS, p=[0.4, 0.25, 0.2, 0.15]))
    df["chosen_model"] = labels

    # ---- outcome字段（约85%已结算）----
    settled = rng.random(n) < 0.85
    df["success"] = [bool(s and rng.random() < 0.92) for s in settled]
    df["latency_ms"] = [
        int(rng.integers(300, 6000)) if s else None for s in settled
    ]
    df["reward"] = [
        round(float(rng.uniform(0.55, 1.0)), 3) if s else None for s in settled
    ]
    return df


def generate_synthetic_annotations(
    df: pd.DataFrame,
    n_annotated: int = 100,
    seed: int = 13,
    wrong_frac: float = 0.4,
) -> pd.DataFrame:
    """从合成数据抽样生成人工标注CSV（llm-gw-annotator格式）。

    wrong_frac比例的样本human_label≠chosen_model（模拟真实标注纠错）。
    """
    rng = np.random.default_rng(seed)
    n = min(int(n_annotated), len(df))
    sample = df.sample(n=n, random_state=seed)

    rows = []
    for _, row in sample.iterrows():
        auto = str(row["chosen_model"])
        if rng.random() < wrong_frac:
            human = rng.choice([m for m in MODELS if m != auto])
        else:
            human = auto
        rows.append({
            "request_id": row["request_id"],
            "model_name": auto,
            "task_type": row["task_type"],
            "auto_provider": auto,
            "confidence": float(row["confidence"]),
            "human_provider": human,
            "is_correct": "true" if human == auto else "false",
            "reason": "correct" if human == auto else rng.choice(
                ["performance", "cost", "availability", "quality"]),
            "annotator": f"annotator_{rng.integers(1, 4)}",
        })
    return pd.DataFrame(rows)
