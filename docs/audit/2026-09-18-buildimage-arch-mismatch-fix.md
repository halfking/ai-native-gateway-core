# 2026-09-18 本地部署构建镜像架构错位审计与修复

## 现象

Apple Silicon (arm64) macOS 本地部署日志出现：

```
[image] hit docker local cache: kx-base/golang:1.27-alpine-amd64
```

用户质疑：本机是 arm64，为何查找 amd64 镜像（前天还正常）。

## 根因（三层叠加，含诚实结论）

1. **硬编码默认名**：91a14466a（2026-09-09）将 `deploy-local.sh` 的
   `LLM_GATEWAY_BUILD_IMAGE` 默认值硬编码为 `kx-base/golang:1.27-alpine-amd64`
   （当时的动机是 deploy-seamless 目标机是 x86，本地脚本被一并改掉）。
   HEAD（301fa21b5）仍是此值——用户运行的是已提交版本，非工作区版本。

2. **本地 Docker 缓存错位 tag（历史事故残留）**：审计时实测，本机缓存中
   `kx-base/golang:1.27-alpine-amd64`、`-arm64`、`-amd64-stale-cache` 三个 tag
   共享同一镜像 ID `2daeca9aa39a`，且该 ID 实际架构为 **linux/arm64**——
   即 `-amd64` 名字下挂的是 arm64 镜像。成因是早期某次运行用 `-amd64`
   请求名 + linux/arm64 平台加载了 arm64 离线 tar，SSOT 的 tag 回填
   （`_finalize_loaded_tar`）把 arm64 镜像打上了 `-amd64` 名字。

3. **组合效应与诚实结论**：arm64 Mac 上 `docker_platform=linux/arm64`，
   `_resolve_via_docker_inspect` 校验的是**实际架构**（arm64 == arm64，通过），
   只是日志打印的 tag 名是 `-amd64`。因此**用户当时的构建实际使用的是正确的
   arm64 镜像，构建与部署均成功**；缺陷是"名不副实"造成误导，并且该缓存
   在真正的 x86 主机上会静默产出 amd64 名/arm64 实的镜像。这不是"用错指令集
   导致构建失败"的致命 bug，而是命名与架构错位的混淆性缺陷 + 潜在跨机隐患。

另有一个使裸 tag 无法自救的 SSOT 缺陷：`_platform_tag_variant` 对不带平台
后缀的 tag（如 `kx-base/golang:1.27-alpine`）直接返回空，导致离线 tar /
registry 两层永远拼不出 `1.27-alpine-arm64` 候选名，裸 tag 请求必失败。

## 修复

| 文件 | 改动 |
|---|---|
| `scripts/deploy-local.sh`（本仓库） | 默认构建镜像从平台后缀名改为平台无关 `kx-base/golang:1.27-alpine`，由 `resolve_build_image` 依 `$docker_platform` 推导候选 |
| `deploy-image-resolution.sh`（ai-native-tools/deploy-lib SSOT） | `_platform_tag_variant` 去掉裸 tag 提前返回空分支；裸 tag 现在可推导 `<tag>-<arch>` 候选（离线 tar 与 registry 两层生效） |
| `tests/deploy_local_contract_test.sh`（本仓库） | fixture 补 `sql/migrations/.gitkeep`——dadf1e66f（09-10）加的顶层 exit 64 检查使碰撞测试在干净环境必挂（存量回归，与本次镜像改动无关，已用 stash 对照验证） |

`deploy-seamless.sh` 的硬编码 `linux/amd64` **不动**：154/245 目标机是 x86，
属正确契约。

## 验证证据

- `resolve_build_image kx-base/golang:1.27-alpine linux/arm64`：
  缓存 miss（旧缓存 amd64 名/arm64 实不匹配时正确判 miss）→ 离线 tar
  `kx-base-golang-1.27-alpine-arm64.tar.gz` 加载 → inspect 确认 `linux/arm64`。
- 反向 `linux/amd64`：正确加载 amd64 tar 并把本地 `-amd64` tag 清洗为
  名实相符（旧同 ID 错位镜像被 Docker rename 至空串）。
- `bash -n` 四脚本语法通过。
- `tests/deploy_local_contract_test.sh`：**26/26 PASS**（修复 fixture 后）。
- `tests/deploy_blue_green_contract_test.sh`：PASS（exit 0）。
- `tests/deploy_wrapper_test.sh`：12 passed / 0 failed。
- `tests/deploy_credential_decrypt_verify_test.sh`：passed=5 failed=0。

## 已知失败（存量，非本次引入，stash 对照已证）

- `tests/deploy_nocgo_build_test.sh`：4 FAIL。根因
  `cmd/gateway → admin → autoroute → routingopt → vendor/yalue/onnxruntime_go`
  在 CGO=0 下 build constraints 排除全部文件。CGO=0 断裂自 2026-09-07
  （上游引入 CGO-only 依赖）起即为已知状态，deploy 走容器 CGO 回退；
  该测试守护的"CGO=0 必须可构建"契约已过时。**处置待定**：要么给
  onnxruntime 接入层加 `//go:build cgo` 拆分（动 vendor + routingopt 降级
  路径，独立工程），要么正式废弃该契约。保留红色不篡改，避免掩盖。

## 追加审计：deploy-154.sh / deploy-245.sh（同日第二轮）

**结论：两入口的架构处理正确**——154/245 目标机是 x86，seamless 构建段
`GOARCH=amd64` + `--platform linux/amd64` + 默认镜像 `…-amd64` 是正确契约，
与本地部署的错位 bug 本质不同，**不改**。实测解析链：缓存 `-amd64` tag
再次名不副实（arm64 实，见发现 A）时平台校验正确判 miss → 离线 tar 重载
真 amd64 → 容器内 `uname -m` = `x86_64`。dry-run 端口字面量与
targets.sh 一致（两目标均 active 8781 / candidate 8782）。

**发现 A（实锤升级）：错位 tag 会再生。** 上轮结尾本地缓存已清洗为名实
相符，本轮审计再次观测到 `-amd64` tag 挂回 arm64 镜像（ID 2daeca9aa39a）
——上轮"遗留风险 3"从推测升级为二次观测实锤。确切写手未定位（候选：
SSOT `_finalize_loaded_tar` 跨名回填在裸 tag / 平台后缀名混用下的某条
边；不排除 Docker Desktop 行为）。防线有效（平台校验使行为始终正确，
代价是错位后每次 amd64 请求都重载 165MB tar）。根治 = 收紧回填语义
（跨名回填禁止或加目标名平台校验），影响三个部署入口，留独立工程。

**发现 B（已修）：bash 3.2 空参数炸。** `parse-wrapper-flags.sh` 头部注释
明确承诺兼容 macOS 自带 bash 3.2.57 并为此避开 `local -n`，但
`extract_force_unlock` 无参调用时 `"${_rest[@]}"` 空数组展开在 3.2 +
`set -u` 下直接 `unbound variable`（/bin/bash 实测复现）；两 wrapper 的
`"${ARGS[@]}"` / `"${ARGS[*]}"` 同病。当前环境被 brew bash 5 掩盖。
修复：lib 加 `${#_rest[@]}` 计数守卫；两 wrapper 的 help/dry-run 段包
计数守卫（守卫模式沿用 SSOT deploy-image-resolution.sh:330 先例）。
验证：bash3.2 四探针（无参/带参剥 --force-unlock 透传 --seq/两 wrapper
dry-run/--force-unlock+--dry-run 组合）全过；8 个相关套件回归
wrapper 12/0、154 14/0、host 35/0、readiness、state_machine 14/0、
postcondition 4/4(offline)、lock 51/0、ssh_retry 21/0。

## 遗留风险

1. 本地 `deploy-local.sh status` 实测偶发卡死（timeout 15s exit 124），
   疑点在资源探测（docker info / redis 系统级探测）某一环——与用户报告的
   "部署后期卡住"可能同源，本轮未修，需下一轮定位。
   → **已修（同日 23:06 `468a1ce82`）**：`deploy-local-lib.sh dl_detect_resources`
   的 `docker info` 加 `timeout 5`、`docker compose version` 加 `timeout 3`，
   Docker Desktop 高负载时 status 不再无限挂起；遗留关闭。
2. 蓝绿 cutover 受控重启链最坏 ~195s（3 次 × (60s 验证 + 5s 间隔)），
   性能优化未做。
3. SSOT 的 tag 回填仍可能在"带错误平台后缀的请求名"下再次产生名不副实
   tag（平台校验保证行为正确，但名字会骗人）；平台无关默认名从源头降低
   了触发概率。**第二轮已实锤：清洗后错位会再生（见追加审计发现 A），
   根治需收紧 `_finalize_loaded_tar` 回填语义。**
4. `deploy_local_contract_test` 的 fixture 修复后，`[[ ! -e "$version_install/run" ]]`
   等碰撞前置断言依赖 exit 1 路径，若 bump-version 行为变化需同步。
