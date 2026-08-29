# 流处理验证层流程图

本文档包含 SSE 验证层和 Tool Call 验证层的详细流程图。

---

## 1. SSE Frame Validation Flow

### 1.1 First Frame Validation

```mermaid
flowchart TD
    Start([Upstream Response Received]) --> ReadFirst[Read First SSE Frame]
    ReadFirst --> CheckMiniMax{MiniMax Error?}
    CheckMiniMax -->|Yes| ReturnError[Return Error with Status Code]
    CheckMiniMax -->|No| ValidateFirst{validateSSEDataFrame}
    
    ValidateFirst -->|Valid| ParseFirst[Parse OpenAI Chunk]
    ValidateFirst -->|Invalid| LogFirstError[Log: malformed first SSE frame]
    
    LogFirstError --> RecordFirstMetric[Record Metric: first_frame]
    RecordFirstMetric --> MarkInterrupted1[Mark StreamCapture Interrupted]
    MarkInterrupted1 --> ReturnResumable1[Return Resumable=true]
    ReturnResumable1 --> SurvivalRetry1[Survival Coordinator Retries]
    
    ParseFirst --> CheckContent{Has Content?}
    CheckContent -->|No| CheckEmpty[Empty Stream Gate]
    CheckContent -->|Yes| InitGate[Initialize Commit Gate]
    
    CheckEmpty --> BufferOrFail[Buffer/Return Resumable]
    InitGate --> MainLoop[Enter Main Stream Loop]
    BufferOrFail --> MainLoop
    
    style ValidateFirst fill:#ff9999
    style ReturnResumable1 fill:#99ff99
    style SurvivalRetry1 fill:#9999ff
```

### 1.2 Mid-Stream Validation

```mermaid
flowchart TD
    LoopStart([For Each SSE Line]) --> NormalizeLine[Normalize & Split Combined Frames]
    NormalizeLine --> ValidateMid{validateSSEDataFrame}
    
    ValidateMid -->|Valid| ProcessChunk[Process Chunk]
    ValidateMid -->|Invalid| LogMidError[Log: malformed mid-stream]
    
    LogMidError --> RecordMidMetric[Record Metric: mid_stream]
    RecordMidMetric --> CheckCommitted{Client Visible?}
    
    CheckCommitted -->|Not Committed| MarkInterrupted2[Mark StreamCapture Interrupted]
    CheckCommitted -->|Committed| DropFrame[Drop Bad Frame]
    
    MarkInterrupted2 --> ReturnResumable2[Return Resumable=true]
    ReturnResumable2 --> SurvivalRetry2[Survival Coordinator Retries]
    
    DropFrame --> ContinueLoop[Continue Loop]
    ContinueLoop --> LoopStart
    
    ProcessChunk --> ParseChunk[Parse OpenAI Chunk]
    ParseChunk --> WriteToClient[Write to Client via Commit Gate]
    WriteToClient --> ContinueLoop
    
    style ValidateMid fill:#ff9999
    style CheckCommitted fill:#ffff99
    style ReturnResumable2 fill:#99ff99
    style DropFrame fill:#ff9999
```

### 1.3 Complete Validation Decision Tree

```mermaid
flowchart TD
    Frame[Receive SSE Frame] --> Extract[Extract Payload]
    Extract --> CheckEmpty{Payload Empty or [DONE]?}
    
    CheckEmpty -->|Yes| Valid1[✅ Valid]
    CheckEmpty -->|No| CheckJSON{Starts with '{'?}
    
    CheckJSON -->|No| Valid2[✅ Valid - Not JSON]
    CheckJSON -->|Yes| Unmarshal{json.Unmarshal}
    
    Unmarshal -->|Success| Valid3[✅ Valid JSON]
    Unmarshal -->|Error| Invalid[❌ Malformed]
    
    Invalid --> CheckStage{Processing Stage?}
    CheckStage -->|First Frame| FirstAction[Return Resumable=true<br/>No Client Output]
    CheckStage -->|Mid-Stream| CheckVis{Client Visible?}
    
    CheckVis -->|No| MidNotCommitted[Return Resumable=true<br/>chunkCount preserved]
    CheckVis -->|Yes| MidCommitted[Drop Frame<br/>Continue Processing]
    
    Valid1 --> Process[Continue Normal Processing]
    Valid2 --> Process
    Valid3 --> Process
    
    FirstAction --> Survival1[Survival Coordinator<br/>Transparent Retry]
    MidNotCommitted --> Survival2[Survival Coordinator<br/>Transparent Retry]
    MidCommitted --> Process
    
    style Invalid fill:#ff6666
    style Valid1 fill:#66ff66
    style Valid2 fill:#66ff66
    style Valid3 fill:#66ff66
    style FirstAction fill:#99ff99
    style MidNotCommitted fill:#99ff99
    style MidCommitted fill:#ffaa66
```

---

## 2. Tool Call Validation Flow (Planned)

### 2.1 Tool Use/Result Tracking

```mermaid
stateDiagram-v2
    [*] --> Idle
    Idle --> Pending: content_block_start(tool_use)
    Pending --> Executing: tool execution started
    Executing --> Completed: content_block_start(tool_result)
    Completed --> [*]: stream ends normally
    
    Pending --> Incomplete: stream ends
    Executing --> Incomplete: stream interrupted
    Incomplete --> [*]: Return Resumable=true
    
    note right of Incomplete
        Missing tool_result
        Triggers survival retry
    end note
```

### 2.2 Tool Call Validation Process

```mermaid
flowchart TD
    Start([Stream Processing]) --> InitValidator[Initialize Tool Call Validator]
    InitValidator --> ProcessEvents[Process SSE Events]
    
    ProcessEvents --> CheckEvent{Event Type?}
    CheckEvent -->|content_block_start| CheckBlockType{Block Type?}
    CheckEvent -->|content_block_delta| CheckDeltaType{Delta Type?}
    CheckEvent -->|other| ContinueProcess[Continue]
    
    CheckBlockType -->|tool_use| RegisterToolUse[validator.OnToolUse id]
    CheckBlockType -->|tool_result| SkipRegister[Skip - Result Handled in Delta]
    CheckBlockType -->|other| ContinueProcess
    
    RegisterToolUse --> AddToPending[Add to pendingToolUses map]
    AddToPending --> ContinueProcess
    
    CheckDeltaType -->|tool_result| MarkComplete[validator.OnToolResult id]
    CheckDeltaType -->|other| ContinueProcess
    
    MarkComplete --> SetCompleted[Set state.Completed = true]
    SetCompleted --> ContinueProcess
    
    ContinueProcess --> MoreEvents{More Events?}
    MoreEvents -->|Yes| ProcessEvents
    MoreEvents -->|No| ValidateComplete{validator.ValidateComplete}
    
    ValidateComplete -->|All Complete| ReturnSuccess[Return Success]
    ValidateComplete -->|Has Incomplete| LogIncomplete[Log: incomplete tool call]
    
    LogIncomplete --> RecordMetric[Record Metric: incomplete_tool_call]
    RecordMetric --> MarkInterrupted[Mark StreamCapture Interrupted]
    MarkInterrupted --> ReturnResumable[Return Resumable=true]
    ReturnResumable --> SurvivalRetry[Survival Coordinator Retries]
    
    style RegisterToolUse fill:#9999ff
    style MarkComplete fill:#99ff99
    style ValidateComplete fill:#ff9999
    style ReturnResumable fill:#99ff99
```

### 2.3 Tool Call State Machine Detail

```mermaid
flowchart TD
    subgraph "Tool Use Lifecycle"
        A[Stream Starts] --> B[content_block_start type=tool_use]
        B --> C[Register in Validator]
        C --> D{Tool Execution}
        D --> E[content_block_delta type=text_delta]
        E --> F[Tool execution output]
        F --> G[content_block_start type=tool_result]
        G --> H[Mark as Completed]
        H --> I[Stream Continues]
    end
    
    subgraph "Failure Scenarios"
        D -.->|Stream Interrupted| J[Stream Ends Early]
        F -.->|No tool_result| J
        J --> K[ValidateComplete Fails]
        K --> L[Return Resumable=true]
    end
    
    style B fill:#9999ff
    style G fill:#99ff99
    style K fill:#ff9999
    style L fill:#99ff99
```

---

## 3. Integration with Survival Coordinator

### 3.1 Survival Coordinator Decision Flow

```mermaid
flowchart TD
    StreamOutcome[Stream Returns Outcome] --> CheckInterrupted{Interrupted?}
    
    CheckInterrupted -->|No| Success[Normal Success Path]
    CheckInterrupted -->|Yes| CheckResumable{Resumable?}
    
    CheckResumable -->|No| ReturnError[Return Error to Client]
    CheckResumable -->|Yes| CheckWindow{Within Holdback Window?}
    
    CheckWindow -->|No| ReturnError
    CheckWindow -->|Yes| CheckCandidate{More Candidates?}
    
    CheckCandidate -->|No| ReturnError
    CheckCandidate -->|Yes| DiscardBuffer[Discard Buffered Output]
    
    DiscardBuffer --> LogRetry[Log: survival retry]
    LogRetry --> NextCandidate[Try Next Candidate]
    NextCandidate --> NewStream[Start New Stream]
    
    NewStream --> CheckResult{Result?}
    CheckResult -->|Success| ClientOutput[Send to Client]
    CheckResult -->|Failure| CheckMore{More Candidates?}
    
    CheckMore -->|Yes| NextCandidate
    CheckMore -->|No| ReturnError
    
    style CheckResumable fill:#ffff99
    style DiscardBuffer fill:#ff9999
    style NextCandidate fill:#9999ff
    style ClientOutput fill:#99ff99
```

### 3.2 Holdback Window Concept

```mermaid
gantt
    title Holdback Window and Retry Strategy
    dateFormat X
    axisFormat %L ms
    
    section Stream 1
    Upstream Response   :0, 100
    Buffering          :100, 300
    Validation Fail    :300, 301
    
    section Holdback
    Window Active      :0, 500
    Decision Point     :crit, 301, 302
    
    section Stream 2
    Retry Start        :302, 402
    Buffering          :402, 600
    Success            :600, 601
    
    section Client
    No Output Yet      :0, 600
    First Byte         :milestone, 601, 0
```

---

## 4. Performance Characteristics

### 4.1 Validation Overhead

```mermaid
graph LR
    A[SSE Frame] --> B{Validation}
    B -->|~90ns| C[Invalid - Fast Path]
    B -->|~1µs| D[Valid - Full Parse]
    
    D --> E[Normal Processing<br/>~10µs]
    C --> F[Early Return<br/>Resumable=true]
    
    style B fill:#ffff99
    style C fill:#ff9999
    style D fill:#99ff99
```

### 4.2 End-to-End Latency Impact

```mermaid
graph TD
    subgraph "Without Validation"
        A1[Upstream] -->|100ms| B1[Gateway]
        B1 -->|5ms parse| C1[Client]
        C1 -.->|Parse Error| D1[User Sees Error]
    end
    
    subgraph "With Validation"
        A2[Upstream] -->|100ms| B2[Gateway]
        B2 -->|5ms parse + 0.001ms validate| C2[Valid?]
        C2 -->|Yes| D2[Client]
        C2 -->|No| E2[Retry]
        E2 -->|100ms| F2[Client]
        F2 -.->|Success| G2[User Happy]
    end
    
    style C2 fill:#ffff99
    style E2 fill:#9999ff
    style G2 fill:#99ff99
```

---

## 5. Monitoring Flow

### 5.1 Metrics Pipeline

```mermaid
flowchart LR
    A[Validation Event] --> B{Result?}
    B -->|Malformed| C[RecordMalformedSSEFrame]
    B -->|Valid| D[No Metric]
    
    C --> E[Prometheus Counter]
    E --> F[llm_gateway_malformed_sse_frame_total<br/>provider, stage]
    
    F --> G[Grafana Dashboard]
    F --> H[Alert Manager]
    
    H --> I{Rate > Threshold?}
    I -->|Yes| J[Send Alert]
    I -->|No| K[Monitor]
    
    style C fill:#ff9999
    style E fill:#9999ff
    style J fill:#ff6666
```

### 5.2 Observability Stack

```mermaid
graph TD
    subgraph "Gateway"
        A[SSE Validator] --> B[Metrics Interface]
        B --> C[Prometheus Exporter]
    end
    
    subgraph "Monitoring"
        C --> D[Prometheus Server]
        D --> E[Grafana]
        D --> F[Alert Manager]
    end
    
    subgraph "Alerting"
        F --> G[PagerDuty]
        F --> H[Slack]
        F --> I[Email]
    end
    
    subgraph "Logs"
        A --> J[slog]
        J --> K[journald]
        K --> L[Log Aggregation]
    end
    
    style A fill:#ffff99
    style D fill:#9999ff
    style F fill:#ff9999
```

---

## 6. Deployment Flow

### 6.1 Safe Deployment Process

```mermaid
flowchart TD
    Start([Code Complete]) --> UnitTest[Run Unit Tests]
    UnitTest --> IntTest[Run Integration Tests]
    IntTest --> Build[Build Binary]
    
    Build --> Deploy245[Deploy to 245 Test Env]
    Deploy245 --> Monitor245[Monitor 48 Hours]
    
    Monitor245 --> Check245{Issues Found?}
    Check245 -->|Yes| Debug[Debug and Fix]
    Check245 -->|No| GoDecision{Go/No-Go Decision}
    
    Debug --> UnitTest
    
    GoDecision -->|No-Go| Wait[Wait and Monitor]
    GoDecision -->|Go| PrepProd[Prepare 154 Production]
    
    Wait --> Monitor245
    
    PrepProd --> Backup[Backup Current Binary]
    Backup --> Deploy154[Deploy to 154]
    Deploy154 --> Verify[Health Check]
    
    Verify --> VerifyResult{Healthy?}
    VerifyResult -->|No| Rollback[Quick Rollback]
    VerifyResult -->|Yes| MonitorProd[Monitor Production]
    
    Rollback --> Restore[Restore Backup]
    Restore --> Investigate[Investigate]
    
    MonitorProd --> Success[Deployment Complete]
    
    style Check245 fill:#ffff99
    style GoDecision fill:#ff9999
    style VerifyResult fill:#ffff99
    style Rollback fill:#ff6666
    style Success fill:#99ff99
```

---

**Created**: 2026-08-29  
**Format**: Mermaid Diagrams  
**Usage**: Can be rendered in GitHub, GitLab, or any Mermaid-compatible viewer
