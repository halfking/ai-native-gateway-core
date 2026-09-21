# Go 侧模型推理集成

## 目标

将结构化特征分类模型以可控、可回退的方式接入 Go。模型输入是固定 schema 的数值/枚举/布尔特征，不是 prompt、messages 或 response；推理服务不需要持久化请求正文。

## 推理架构

```text
请求生命周期内提取结构化特征
          -> 规则基线 / 结构化模型
          -> 置信度校准与阈值判断
          -> 低置信度或异常时回退规则
          -> 现有 AUTO 路由与 auto_route_selections 记录
```

## 模型接口约定

模型包必须携带：模型版本、特征 schema version、特征名称与顺序、类别映射、归一化参数、校准参数、训练集版本和评估摘要。Go 侧在加载时校验 schema、维度和版本，不匹配则拒绝加载并保留上一版本。

伪代码：

```go
type StructuredFeatures struct {
    SchemaVersion string
    Language      int
    LengthBucket  int
    ContextBucket int
    TurnBucket    int
    HasCode       bool
    HasMath       bool
    HasTable      bool
    ComplexityBin int
}

type Predictor interface {
    Predict(ctx context.Context, features StructuredFeatures) (task string, confidence float64, err error)
}
```

## ONNX 使用边界

若采用 ONNX，导出的是结构化特征模型及其必要的预处理/校准参数；不要在 Go 中实现文本 tokenizer，也不要为推理缓存正文。ONNX Runtime、纯 Go 推理或其他实现均需以实际基准选择，性能数字不能写成保证。

## 缓存与观测

可按不可逆特征 fingerprint 和模型版本做短 TTL 缓存，但缓存值只能是分类结果与版本信息，不能包含正文、摘要、响应或可逆特征。记录模型版本、schema 版本、推理耗时、回退原因、置信度桶和错误类型；日志不得输出输入内容。

## 发布与回退

模型热加载必须先校验文件完整性、schema 和类别映射，再原子切换；加载失败继续使用当前版本。灰度期间比较规则基线与模型的人工标签一致性、覆盖率、错误率、延迟和资源占用；不达标即回退。所有结果最终仍通过现有 `auto_route_selections` 事实链路观测。
