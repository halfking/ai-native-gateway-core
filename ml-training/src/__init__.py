"""P2.4 ML训练管道（AUTO路由）。

模块:
    data_loader       Parquet加载 + 人工标注合并 + 清洗 + 数据集划分
    feature_pipeline  类别编码 / 布尔转换 / 数值归一化（sklearn Pipeline）
    train             RandomForest基线训练
    evaluate          指标、混淆矩阵、特征重要性、基线对比
    export_model      joblib / ONNX 导出
    synthetic         合成数据生成（测试/冒烟）
"""

__version__ = "0.1.0"
