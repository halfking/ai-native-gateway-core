# Multi-Layer Security Detection System - Design Document

## Overview

This is a **modular, three-layer security detection system** for both **output compliance checking** and **input injection detection**, designed to significantly reduce false positives while maintaining high accuracy and performance.

## Problem Statement

Current security detection relies heavily on regex patterns, which leads to:
- **High false positive rate** (~20-30%): Chrome IDs flagged as credit cards, git remotes as emails, code examples as real credentials
- **No context awareness**: Cannot distinguish between examples and real threats
- **Binary decisions**: No confidence scoring or escalation
- **Performance vs accuracy trade-off**: Either fast+inaccurate or slow+accurate

## Solution: Multi-Layer Architecture

### Layer 1: Rule-Based Detection (Fast Path)
- **Latency**: <1ms
- **Cost**: $0
- **Precision**: 80-90% (with credibility checks)
- **Strategy**:
  - Compiled regex patterns for known threat signatures
  - **Credibility validators** to reduce false positives:
    - Credit cards: Luhn algorithm validation
    - ID cards: Province code + date validation  
    - Phone numbers: Sequential/repeating filter
    - API keys: Context markers (examples, placeholders)
  - Confidence scoring based on match quality

### Layer 2: Context-Based Validation (Balanced)
- **Latency**: 1-10ms
- **Cost**: $0
- **Precision**: 85-95%
- **Strategy**:
  - Semantic analysis of surrounding context
  - Session history (similar threats in conversation)
  - Structural validation (JSON keys, code blocks, tool outputs)
  - False positive reduction through contextual rules

### Layer 3: LLM-Based Validation (High Accuracy)
- **Latency**: 100-500ms
- **Cost**: ~$0.0001/request
- **Precision**: 95-99%
- **Strategy**:
  - Semantic understanding of intent
  - Handles ambiguous cases (e.g., "my password is 'example123'" vs real password)
  - Only invoked for low-confidence detections

## Key Improvements Over Current System

### 1. False Positive Reduction

**Before** (pure regex):
```
Input: "Chrome启动参数 --field-trial-handle=1234567890123456"
Detection: ❌ Credit card detected (16 digits)
Result: Blocked legitimate command
```

**After** (multi-layer):
```
Layer 1: Pattern match (confidence: 0.6)
Layer 2: Context check → Chrome CLI argument → False positive
Result: ✅ Allowed with confidence 0.92
```

### 2. Confidence-Based Escalation

```
High confidence (>0.85) → Accept result, no escalation
Medium confidence (0.65-0.85) → Layer 2 validation
Low confidence (<0.65) → Layer 3 LLM validation
```

### 3. Cost Optimization

- **80-85%** of requests handled by Layer 1 (zero cost)
- **10-15%** escalate to Layer 2 (zero cost)
- **5%** escalate to Layer 3 (minimal cost)
- **Average cost**: ~$0.00005/request (vs $0 pure regex or $0.0003 pure LLM)

## Architecture Components

### Core Interfaces

```go
// SecurityDetector - Pluggable detection layer
type SecurityDetector interface {
    Detect(ctx, content, detectionCtx) (*SecurityResult, error)
    Name() string
    Layer() int
}

// SecurityResult - Unified output
type SecurityResult struct {
    IsSafe       bool
    Confidence   float64  // 0.0-1.0
    Threats      []ThreatMatch
    Layer        string
    ShouldBlock  bool
    ShouldRedact bool
    RedactedText string
}
```

### Detection Context

```go
type DetectionContext struct {
    // Content metadata
    Language string
    IsSystemPrompt bool
    IsToolOutput bool
    
    // Session context
    SessionID string
    TenantID string
    PreviousThreats []ThreatType
    
    // Technical context  
    ModelName string
    MessageIndex int
}
```

### Supported Threat Types

**Output Compliance**:
- PII: credit_card, email, phone, ssn, id_card
- Secrets: password, api_key, internal_ip
- Content: toxicity, bias, hallucination

**Input Injection**:
- prompt_injection (instruction override)
- jailbreak (role manipulation)
- system_prompt_leak (extraction attempts)
- instruction_override

## Implementation Plan

### Phase 1: Core Framework ✅ (Completed)
- [x] Interface definitions
- [x] Layer 1: Rule-based detector with credibility checks
- [x] Threat pattern library
- [x] Confidence scoring

### Phase 2: Context Layer (In Progress)
- [ ] Context validator implementation
- [ ] Session history tracking
- [ ] Structural analysis (JSON, code blocks)
- [ ] False positive reduction rules

### Phase 3: LLM Layer
- [ ] LLM provider integration (GLM-4-flash)
- [ ] Prompt engineering for threat validation
- [ ] Cost optimization (batching, caching)

### Phase 4: Pipeline Orchestrator
- [ ] Chained detector with escalation
- [ ] Result caching (content hash)
- [ ] Performance metrics tracking
- [ ] Integration with existing security modules

### Phase 5: Production Integration
- [ ] Bridge to existing sanitize/outputcompliance
- [ ] Backward compatibility adapter
- [ ] A/B testing framework
- [ ] Monitoring and alerting

## Performance Targets

### Latency
- **P50**: <1ms (Layer 1 handles 85%)
- **P95**: <5ms (Layer 2 handles 14%)
- **P99**: <200ms (Layer 3 handles 1%)

### Accuracy
- **False Positive Rate**: <5% (down from 20-30%)
- **False Negative Rate**: <2% (maintain current level)
- **Overall Precision**: 95%+ (up from 70-80%)

### Cost
- **Layer 1**: $0
- **Layer 2**: $0
- **Layer 3**: ~$0.0001/request
- **Average**: ~$0.00005/request (only 5% use LLM)

## Integration Examples

### Drop-in Replacement for Current System

```go
// Old code (pure regex, high false positives)
detector := sanitize.NewDetector(patterns)
fragments, _ := detector.Detect(ctx, text)

// New code (multi-layer, low false positives)
pipeline := security.NewSecurityPipeline(
    ruleDetector,
    contextValidator,
    llmDetector,
    config,
)
result, _ := pipeline.Detect(ctx, text, detectionCtx)
```

### Zero-Cost Mode (Rule + Context Only)

```go
// For cost-sensitive deployments
pipeline := security.NewSecurityPipeline(
    ruleDetector,
    contextValidator,
    nil, // No LLM layer
    config,
)
```

### Strict Mode (Always Validate with LLM)

```go
// For high-security deployments
config := security.SecurityPipelineConfig{
    StrictMode: true, // Force LLM validation even for high-confidence matches
    ConfidenceThreshold: 0.95,
}
```

## Migration Strategy

### Phase 1: Shadow Mode (Week 1-2)
- Deploy alongside existing system
- Log decisions from both systems
- Compare false positive rates
- Tune confidence thresholds

### Phase 2: Gradual Rollout (Week 3-4)
- 10% traffic → multi-layer system
- Monitor metrics (latency, FP rate, cost)
- Increase to 50%, then 100%

### Phase 3: Full Migration (Week 5+)
- Replace existing system
- Keep old system as fallback
- Continuous monitoring and tuning

## Monitoring Metrics

### Detection Metrics
- Threats detected per type
- False positive rate (requires ground truth)
- False negative rate
- Confidence distribution

### Performance Metrics
- P50/P95/P99 latency per layer
- Layer usage distribution (% using L1/L2/L3)
- Cache hit rate
- Average cost per request

### Business Metrics
- User complaints about false positives
- Security incidents prevented
- Cost savings vs pure LLM approach

## Future Enhancements

1. **Federated Learning**: Learn from production data to improve patterns
2. **Active Learning**: Route uncertain cases to human reviewers
3. **Multi-Model Ensemble**: Use multiple LLMs for cross-validation
4. **Real-time Adaptation**: Adjust thresholds based on observed FP rate
5. **Explainability**: Generate human-readable explanations for decisions

## Success Criteria

✅ **False Positive Rate**: Reduce from 20-30% to <5%
✅ **Latency**: P95 <5ms (vs current <1ms pure regex)
✅ **Cost**: <$0.0001/request average
✅ **Accuracy**: Maintain 98%+ true positive rate
✅ **Compatibility**: Drop-in replacement for existing system

---

**Status**: Framework designed and Layer 1 implemented
**Next Step**: Implement Layer 2 (Context Validator)
**Target**: Production-ready in 4-5 weeks
