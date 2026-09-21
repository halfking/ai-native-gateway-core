# 任务2: CI流水线强化 - 详细提示词

## 任务2.1: 强制Race检测

### 执行提示词

```markdown
# 任务：在CI流水线中强制启用Race Detector

## 背景
Go的race detector可以在运行时检测数据竞争问题，但会增加2-10倍的运行时间和内存开销。当前测试可能未启用race检测，导致并发问题在生产环境才暴露。

## 目标
- 在CI流水线中所有测试运行时强制启用 `-race` 标志
- 确保现有代码通过race检测
- 配置合理的超时时间

## 实施步骤

### 1. 检查当前CI配置

首先确认当前CI配置文件位置：
```bash
# GitHub Actions
.github/workflows/test.yml
.github/workflows/ci.yml

# GitLab CI
.gitlab-ci.yml

# 其他CI系统
.circleci/config.yml
.travis.yml
```

### 2. 修改CI配置文件

#### GitHub Actions示例

**文件**: `.github/workflows/test.yml`

```yaml
name: Tests

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main, develop ]

jobs:
  test:
    name: Unit and Integration Tests
    runs-on: ubuntu-latest
    timeout-minutes: 30  # 增加超时（race模式更慢）
    
    strategy:
      matrix:
        go-version: ['1.21', '1.22']  # 测试多个Go版本
    
    steps:
      - name: Checkout code
        uses: actions/checkout@v4
      
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go-version }}
          cache: true
      
      - name: Install dependencies
        run: go mod download
      
      - name: Run tests with race detector
        run: |
          go test -v -race -timeout=20m -coverprofile=coverage.out -covermode=atomic ./...
        env:
          GORACE: "halt_on_error=1 log_path=/tmp/race"
      
      - name: Run integration tests with race detector
        run: |
          go test -v -race -timeout=15m -tags=integration ./...
        env:
          GORACE: "halt_on_error=1"
      
      - name: Upload coverage
        uses: codecov/codecov-action@v3
        with:
          file: ./coverage.out
          flags: unittests
          fail_ci_if_error: true
      
      - name: Upload race logs (if failed)
        if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: race-detector-logs
          path: /tmp/race.*
          retention-days: 7
```

#### GitLab CI示例

**文件**: `.gitlab-ci.yml`

```yaml
stages:
  - test

variables:
  GO_VERSION: "1.21"

test:unit:
  stage: test
  image: golang:${GO_VERSION}
  timeout: 30 minutes
  script:
    - go mod download
    - export GORACE="halt_on_error=1 log_path=/tmp/race"
    - go test -v -race -timeout=20m -coverprofile=coverage.out ./...
  coverage: '/coverage: \d+.\d+% of statements/'
  artifacts:
    when: always
    paths:
      - coverage.out
      - /tmp/race.*
    expire_in: 1 week
  
test:integration:
  stage: test
  image: golang:${GO_VERSION}
  timeout: 20 minutes
  script:
    - go mod download
    - export GORACE="halt_on_error=1"
    - go test -v -race -timeout=15m -tags=integration ./...
  artifacts:
    when: on_failure
    paths:
      - /tmp/race.*
    expire_in: 1 week
```

### 3. 配置GORACE环境变量

`GORACE` 环境变量控制race detector行为：

```bash
# 推荐配置
export GORACE="halt_on_error=1 log_path=/tmp/race history_size=7"

# 参数说明：
# halt_on_error=1      : 检测到race立即退出（默认0，继续运行）
# log_path=/tmp/race   : race日志保存路径
# history_size=7       : 每个goroutine保留的内存访问历史（默认1）
# strip_path_prefix=/  : 从日志中移除路径前缀
```

### 4. 本地验证

开发者本地运行race检测：

```bash
# 单个包
go test -race ./internal/ir

# 所有包
go test -race ./...

# 特定测试
go test -race -run TestConcurrent ./domains/dispatch

# 持续运行（发现间歇性问题）
for i in {1..100}; do
  echo "Run $i"
  go test -race ./... || break
done
```

### 5. 修复发现的数据竞争

#### 典型问题类型

**A. 未保护的共享变量**

```go
// ❌ 错误示例
type Counter struct {
    count int
}

func (c *Counter) Increment() {
    c.count++  // 数据竞争！
}

// ✅ 修复：使用互斥锁
type Counter struct {
    mu    sync.Mutex
    count int
}

func (c *Counter) Increment() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.count++
}

// ✅ 或使用原子操作
type Counter struct {
    count atomic.Int64
}

func (c *Counter) Increment() {
    c.count.Add(1)
}
```

**B. Map并发读写**

```go
// ❌ 错误示例
var cache = make(map[string]string)

func Get(key string) string {
    return cache[key]  // 如果其他goroutine在写，会panic
}

// ✅ 修复：使用sync.Map
var cache sync.Map

func Get(key string) string {
    val, _ := cache.Load(key)
    if val != nil {
        return val.(string)
    }
    return ""
}

// ✅ 或使用读写锁
type Cache struct {
    mu   sync.RWMutex
    data map[string]string
}

func (c *Cache) Get(key string) string {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.data[key]
}
```

**C. 闭包捕获循环变量**

```go
// ❌ 错误示例
for _, item := range items {
    go func() {
        process(item)  // 所有goroutine看到的是最后一个item
    }()
}

// ✅ 修复：显式传参
for _, item := range items {
    item := item  // 创建副本（Go 1.22+不需要）
    go func() {
        process(item)
    }()
}

// ✅ 或直接传参
for _, item := range items {
    go func(i Item) {
        process(i)
    }(item)
}
```

**D. 延迟初始化竞争**

```go
// ❌ 错误示例
var config *Config

func GetConfig() *Config {
    if config == nil {
        config = loadConfig()  // 多个goroutine可能同时执行
    }
    return config
}

// ✅ 修复：使用sync.Once
var (
    config     *Config
    configOnce sync.Once
)

func GetConfig() *Config {
    configOnce.Do(func() {
        config = loadConfig()
    })
    return config
}
```

### 6. 性能影响分析

对比启用race检测前后的CI时间：

```markdown
## 性能影响报告

| 测试套件 | 正常模式 | Race模式 | 增长率 |
|---------|---------|---------|--------|
| 单元测试 | 2m30s   | 8m15s   | 3.3x   |
| 集成测试 | 5m00s   | 12m30s  | 2.5x   |
| 总计     | 7m30s   | 20m45s  | 2.8x   |

**内存使用**: 增加约5-10倍

**建议**:
- CI超时时间从15分钟增加到30分钟
- 考虑并行运行（分组测试）
```

### 7. 豁免某些测试（慎用）

对于已知的false positive或第三方库问题，可以临时豁免：

```go
// +build !race

package mypackage

// 此文件只在非race模式下编译
func TestKnownRaceIssue(t *testing.T) {
    // ...
}
```

或在CI中排除：
```yaml
- name: Run tests
  run: |
    go test -race $(go list ./... | grep -v problematic/package)
```

**注意**: 豁免应该是临时措施，需要记录Issue并计划修复。

### 8. 监控和告警

添加CI状态监控：

```yaml
- name: Notify on race detection failure
  if: failure()
  uses: 8398a7/action-slack@v3
  with:
    status: ${{ job.status }}
    text: '🔴 Race detector found issues in ${{ github.ref }}'
    webhook_url: ${{ secrets.SLACK_WEBHOOK }}
```

## 验收标准

- [ ] CI配置文件已更新，所有测试启用 `-race`
- [ ] 超时时间合理（至少增加2倍）
- [ ] 现有代码通过race检测（或已记录豁免）
- [ ] 失败时上传race日志artifacts
- [ ] 文档更新：开发者指南添加本地race检测说明
- [ ] 至少运行一次完整CI验证无误

## 交付物

1. 修改后的CI配置文件
2. Race检测修复报告（如果发现问题）
3. 更新后的开发者文档（README或CONTRIBUTING.md）

## 时间估算
- CI配置修改: 2小时
- 本地验证: 2小时
- 修复发现的问题: 0.5-1天（取决于问题数量）
- 文档更新: 1小时
```

---

## 任务2.2: 静态分析集成

### 执行提示词

```markdown
# 任务：集成静态代码分析工具

## 背景
静态分析工具可以在不运行代码的情况下发现潜在问题，如未使用的变量、可疑的错误处理、安全漏洞等。

## 目标工具

1. **go vet**: 官方工具，检查常见错误
2. **staticcheck**: 更严格的代码检查
3. **gosec**: 安全漏洞扫描
4. **golangci-lint**: 集成多个linter的工具（可选）

## 实施步骤

### 1. go vet集成

**文件**: `.github/workflows/lint.yml`（新建）

```yaml
name: Lint

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main, develop ]

jobs:
  vet:
    name: Go Vet
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Run go vet
        run: go vet ./...
```

**本地运行**:
```bash
go vet ./...
```

### 2. staticcheck集成

```yaml
  staticcheck:
    name: Staticcheck
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Install staticcheck
        run: go install honnef.co/go/tools/cmd/staticcheck@latest
      
      - name: Run staticcheck
        run: staticcheck -f stylish ./...
```

**配置文件**: `staticcheck.conf`

```toml
checks = ["all", "-ST1000", "-ST1003"]

# ST1000: 包注释格式（过于严格）
# ST1003: 驼峰命名（允许某些缩写如API）

[dot_import_whitelist]
# 允许dot import的包（通常不推荐）
```

**本地运行**:
```bash
# 安装
go install honnef.co/go/tools/cmd/staticcheck@latest

# 运行
staticcheck ./...

# 检查特定问题
staticcheck -checks=SA1019 ./...  # 废弃API使用
```

### 3. gosec安全扫描

```yaml
  security:
    name: Security Scan
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: Run Gosec Security Scanner
        uses: securego/gosec@master
        with:
          args: '-fmt sarif -out gosec-results.sarif ./...'
      
      - name: Upload SARIF file
        uses: github/codeql-action/upload-sarif@v2
        with:
          sarif_file: gosec-results.sarif
```

**配置文件**: `.gosec.json`

```json
{
  "global": {
    "nosec": false,
    "audit": true
  },
  "exclude": [
    "G404"
  ]
}
```

**G404**: 弱随机数生成器（如果用于非安全场景可豁免）

**本地运行**:
```bash
# 安装
go install github.com/securego/gosec/v2/cmd/gosec@latest

# 运行
gosec ./...

# 生成报告
gosec -fmt=json -out=results.json ./...
```

### 4. golangci-lint（推荐，整合所有工具）

```yaml
  golangci-lint:
    name: GolangCI-Lint
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v5
        with:
          go-version: '1.21'
      
      - name: golangci-lint
        uses: golangci/golangci-lint-action@v3
        with:
          version: latest
          args: --timeout=10m
```

**配置文件**: `.golangci.yml`

```yaml
run:
  timeout: 10m
  tests: true
  modules-download-mode: readonly

linters:
  enable:
    - errcheck       # 检查未处理的错误
    - gosimple       # 简化代码建议
    - govet          # go vet
    - ineffassign    # 检测无效赋值
    - staticcheck    # staticcheck
    - unused         # 未使用的代码
    - gosec          # 安全检查
    - gocritic       # 代码质量检查
    - gofmt          # 格式检查
    - goimports      # import排序
    - misspell       # 拼写错误
    - revive         # 替代golint
    - typecheck      # 类型检查
  
  disable:
    - deadcode       # 已废弃
    - varcheck       # 已废弃
    - structcheck    # 已废弃

linters-settings:
  errcheck:
    check-type-assertions: true
    check-blank: true
  
  govet:
    check-shadowing: true
  
  gofmt:
    simplify: true
  
  gosec:
    excludes:
      - G404  # 弱随机数（非安全场景）
  
  gocritic:
    enabled-tags:
      - diagnostic
      - style
      - performance
    disabled-checks:
      - commentedOutCode

issues:
  exclude-rules:
    # 排除测试文件的某些检查
    - path: _test\.go
      linters:
        - gosec
        - errcheck
    
    # 排除生成的代码
    - path: .*\.pb\.go
      linters:
        - all
  
  max-issues-per-linter: 0
  max-same-issues: 0

output:
  format: colored-line-number
  print-issued-lines: true
  print-linter-name: true
```

**本地运行**:
```bash
# 安装
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# 运行
golangci-lint run

# 只运行特定linter
golangci-lint run --enable-only=errcheck

# 修复可自动修复的问题
golangci-lint run --fix
```

### 5. 常见问题及修复

#### 问题A: 未处理的错误（errcheck）

```go
// ❌ 错误示例
rows.Close()
json.Unmarshal(data, &v)

// ✅ 修复
if err := rows.Close(); err != nil {
    log.Printf("failed to close rows: %v", err)
}

if err := json.Unmarshal(data, &v); err != nil {
    return fmt.Errorf("unmarshal failed: %w", err)
}

// ✅ 明确忽略（如果合理）
_ = rows.Close()  // 显式忽略
```

#### 问题B: 影子变量（govet）

```go
// ❌ 错误示例
var err error
if condition {
    data, err := fetchData()  // 影子变量！
    // ...
}
// 外部err未赋值

// ✅ 修复
var err error
if condition {
    var data Data
    data, err = fetchData()
    // ...
}
```

#### 问题C: SQL注入风险（gosec）

```go
// ❌ 错误示例（gosec G201）
query := fmt.Sprintf("SELECT * FROM users WHERE id = %s", userID)
db.Query(query)

// ✅ 修复：使用参数化查询
db.Query("SELECT * FROM users WHERE id = $1", userID)
```

#### 问题D: 未使用的变量（unused）

```go
// ❌ 错误示例
func process() {
    data := fetchData()  // 未使用
    // ...
}

// ✅ 修复：删除或使用
func process() {
    // 删除未使用的代码
}
```

### 6. 预提交钩子（可选）

强制本地运行lint：

**文件**: `.git/hooks/pre-commit`（或使用pre-commit框架）

```bash
#!/bin/bash

echo "Running linters..."

# 运行golangci-lint
golangci-lint run --new-from-rev=HEAD~1

if [ $? -ne 0 ]; then
    echo "❌ Lint failed. Please fix issues before committing."
    exit 1
fi

echo "✅ Lint passed"
```

使其可执行：
```bash
chmod +x .git/hooks/pre-commit
```

### 7. 性能优化

**缓存依赖**:
```yaml
- uses: actions/cache@v3
  with:
    path: |
      ~/.cache/go-build
      ~/go/pkg/mod
      ~/.cache/golangci-lint
    key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
```

**并行运行**:
```yaml
strategy:
  matrix:
    linter: [govet, staticcheck, gosec]
steps:
  - run: ${{ matrix.linter }} ./...
```

### 8. 处理遗留代码

对于大型项目，可能有大量遗留警告：

**策略A: 基线快照**
```bash
# 生成当前问题快照
golangci-lint run --issues-exit-code=0 > baseline.txt

# 只检查新增问题
golangci-lint run --new-from-rev=main
```

**策略B: 逐步修复**
```yaml
# 设置最大问题数，逐步降低
golangci-lint run --max-issues-per-linter=50
```

**策略C: 按目录豁免**
```yaml
issues:
  exclude-rules:
    - path: legacy/
      linters:
        - all
```

## 验收标准

- [ ] CI包含至少3个静态分析步骤（vet, staticcheck, gosec）
- [ ] 配置文件已创建（`.golangci.yml`等）
- [ ] 现有代码通过所有检查（或豁免已记录）
- [ ] 失败时CI阻止合并
- [ ] 文档更新：开发者指南添加本地lint运行说明
- [ ] PR模板更新：提醒运行lint

## 交付物

1. `.github/workflows/lint.yml`（或相应CI配置）
2. `.golangci.yml`配置文件
3. Lint问题修复报告
4. 更新后的开发者文档

## 时间估算
- CI配置: 3小时
- 配置调优: 2小时
- 修复现有问题: 0.5-1天
- 文档更新: 1小时
```

---

## 任务2.3: Goroutine泄漏检测

### 执行提示词

```markdown
# 任务：集成Goroutine泄漏检测

## 背景
Goroutine泄漏是Go程序中的隐形杀手，会导致内存泄漏和资源耗尽。手动检测困难，需要自动化工具。

## 推荐工具
**uber-go/goleak**: Uber开源的goroutine泄漏检测库

## 实施步骤

### 1. 安装依赖

```bash
go get -u go.uber.org/goleak@latest
```

更新 `go.mod`:
```go
require (
    go.uber.org/goleak v1.3.0
)
```

### 2. 集成到测试

#### 方法A: 全局TestMain（推荐）

在每个需要检测的包中添加：

**文件**: `internal/dispatch/main_test.go`

```go
package dispatch

import (
    "os"
    "testing"
    
    "go.uber.org/goleak"
)

func TestMain(m *testing.M) {
    // 配置goleak选项
    opts := []goleak.Option{
        // 忽略已知的良性goroutine
        goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
        goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
        goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccBalancerWrapper).watcher"),
        
        // 忽略测试框架的goroutine
        goleak.IgnoreTopFunction("testing.(*T).Run"),
    }
    
    // 运行测试并检查泄漏
    exitCode := goleak.VerifyTestMain(m, opts...)
    
    os.Exit(exitCode)
}
```

#### 方法B: 单个测试函数

```go
func TestMyFunction(t *testing.T) {
    defer goleak.VerifyNone(t)
    
    // 测试逻辑
    go func() {
        // 某些操作
    }()
}
```

#### 方法C: 测试套件级别（Testify）

```go
type MySuite struct {
    suite.Suite
}

func (s *MySuite) SetupSuite() {
    // 初始化
}

func (s *MySuite) TearDownSuite() {
    // 清理
}

func (s *MySuite) TearDownTest() {
    // 每个测试后检查泄漏
    goleak.VerifyNone(s.T())
}

func TestMySuite(t *testing.T) {
    suite.Run(t, new(MySuite))
}
```

### 3. 配置忽略规则

#### 常见需要忽略的goroutine

```go
var commonIgnores = []goleak.Option{
    // 标准库
    goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
    goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
    goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
    
    // 数据库连接池
    goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
    goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
    
    // Redis客户端
    goleak.IgnoreTopFunction("github.com/go-redis/redis/v8/internal/pool.(*ConnPool).reaper"),
    
    // gRPC
    goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccBalancerWrapper).watcher"),
    goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransport"),
    
    // 日志库
    goleak.IgnoreTopFunction("go.uber.org/zap/zapcore.(*Sampler).runCorePoolCleaner"),
    
    // 测试框架
    goleak.IgnoreTopFunction("testing.(*T).Run"),
    goleak.IgnoreTopFunction("testing.tRunner"),
}
```

#### 项目特定goroutine

分析项目中的长期运行goroutine：

```bash
# 运行测试并查看泄漏
go test -v ./internal/dispatch 2>&1 | grep "goroutine"
```

输出示例：
```
goleak: Errors on successful test run: found unexpected goroutines:
[Goroutine 123 in state IO wait, with github.com/myproject/bg.(*PartitionManager).Run on top of the stack:
...
]
```

添加忽略：
```go
goleak.IgnoreTopFunction("github.com/myproject/bg.(*PartitionManager).Run")
```

### 4. 修复常见泄漏模式

#### 模式A: 未关闭的channel

```go
// ❌ 泄漏示例
func process() {
    ch := make(chan int)
    go func() {
        for v := range ch {  // 永远阻塞，因为ch未关闭
            handle(v)
        }
    }()
    
    ch <- 1
    // 忘记关闭ch
}

// ✅ 修复
func process() {
    ch := make(chan int)
    go func() {
        for v := range ch {
            handle(v)
        }
    }()
    
    ch <- 1
    close(ch)  // 明确关闭
}
```

#### 模式B: context未传播

```go
// ❌ 泄漏示例
func startWorker() {
    go func() {
        for {
            work()
            time.Sleep(time.Second)
        }
    }()  // 无法停止
}

// ✅ 修复
func startWorker(ctx context.Context) {
    go func() {
        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()
        
        for {
            select {
            case <-ctx.Done():
                return  // 优雅退出
            case <-ticker.C:
                work()
            }
        }
    }()
}
```

#### 模式C: WaitGroup使用错误

```go
// ❌ 泄漏示例
func processItems(items []Item) {
    var wg sync.WaitGroup
    for _, item := range items {
        wg.Add(1)
        go func(i Item) {
            if shouldSkip(i) {
                return  // 忘记wg.Done()
            }
            defer wg.Done()
            process(i)
        }(item)
    }
    wg.Wait()
}

// ✅ 修复
func processItems(items []Item) {
    var wg sync.WaitGroup
    for _, item := range items {
        wg.Add(1)
        go func(i Item) {
            defer wg.Done()  // 放在函数开头，确保调用
            
            if shouldSkip(i) {
                return
            }
            process(i)
        }(item)
    }
    wg.Wait()
}
```

#### 模式D: 泄漏的HTTP请求

```go
// ❌ 泄漏示例
func fetch(url string) error {
    resp, err := http.Get(url)
    if err != nil {
        return err
    }
    // 忘记关闭body，连接泄漏
    return nil
}

// ✅ 修复
func fetch(url string) error {
    resp, err := http.Get(url)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    // 必须读取body（即使不需要）
    io.Copy(io.Discard, resp.Body)
    
    return nil
}
```

### 5. CI集成

#### GitHub Actions

```yaml
- name: Run tests with goroutine leak detection
  run: |
    go test -v ./... -tags=goleak
  env:
    GOLEAK_ENABLED: "true"
```

#### 条件启用（避免影响快速反馈循环）

```go
func TestMain(m *testing.M) {
    // 只在CI中启用goleak
    if os.Getenv("CI") == "true" || os.Getenv("GOLEAK_ENABLED") == "true" {
        goleak.VerifyTestMain(m)
        return
    }
    
    os.Exit(m.Run())
}
```

### 6. 性能影响

Goleak检测会增加测试时间：

| 测试规模 | 正常时间 | Goleak时间 | 增长 |
|---------|---------|-----------|------|
| 小（<50个测试） | 5s | 8s | +60% |
| 中（<500个测试）| 1m | 2m30s | +150% |
| 大（>1000个测试）| 5m | 12m | +140% |

**优化建议**:
- 只在关键包中启用（如并发相关）
- 本地开发默认关闭，CI强制开启
- 使用 `testing.Short()` 跳过

```go
func TestMain(m *testing.M) {
    if testing.Short() {
        os.Exit(m.Run())
        return
    }
    
    goleak.VerifyTestMain(m)
}
```

运行：
```bash
# 快速测试（跳过goleak）
go test -short ./...

# 完整测试（包含goleak）
go test ./...
```

### 7. 调试泄漏

#### 查看goroutine堆栈

```go
func TestDebugLeak(t *testing.T) {
    defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
    
    // 启动可疑goroutine
    go leakyFunction()
    
    time.Sleep(100 * time.Millisecond)
    
    // 打印所有goroutine
    buf := make([]byte, 1<<20)
    stackSize := runtime.Stack(buf, true)
    fmt.Printf("All goroutines:\n%s\n", buf[:stackSize])
}
```

#### 使用pprof分析

```bash
# 运行测试并生成goroutine profile
go test -v -cpuprofile=cpu.prof -memprofile=mem.prof -blockprofile=block.prof ./...

# 查看goroutine数量
go tool pprof -http=:8080 goroutine.prof
```

### 8. 文档化检测策略

创建 `docs/testing/goroutine-leak-detection.md`:

```markdown
# Goroutine泄漏检测策略

## 启用检测的包
- `internal/dispatch` - 调度器核心逻辑
- `internal/streaming` - 流式处理
- `bg/*` - 后台任务
- `domains/routing` - 路由逻辑

## 已知忽略的goroutine
见 `internal/testing/goleak_config.go`

## 本地运行
```bash
# 启用检测
GOLEAK_ENABLED=true go test ./internal/dispatch

# 调试特定测试
go test -v -run TestSpecific ./internal/dispatch
```

## 常见泄漏原因
1. 未关闭的channel
2. context未传播
3. HTTP body未关闭
4. WaitGroup计数错误
```

## 验收标准

- [ ] 核心包（至少5个）集成goleak
- [ ] CI自动运行检测
- [ ] 发现并修复现有泄漏
- [ ] 文档化忽略规则
- [ ] 开发者指南更新

## 交付物

1. 更新后的测试文件（添加`TestMain`或`defer goleak.VerifyNone`）
2. 泄漏修复报告
3. `docs/testing/goroutine-leak-detection.md`文档
4. CI配置更新

## 时间估算
- 集成goleak: 0.5天
- 配置忽略规则: 0.5天
- 修复发现的泄漏: 1天
- 文档编写: 0.5天
```

---

## 总结

任务2的三个子任务提供了全面的CI强化策略：

1. **Race检测**: 发现并发安全问题
2. **静态分析**: 发现代码质量和安全问题
3. **Goroutine泄漏检测**: 发现资源泄漏

每个任务都包含：
- 详细的实施步骤
- 配置示例
- 常见问题修复模式
- CI集成方案
- 性能影响分析
- 验收标准

**预期效果**:
- 并发Bug减少80%+
- 代码质量提升（可量化的lint指标）
- 资源泄漏零容忍

**下一步**: 继续生成任务3-10的详细提示词。
