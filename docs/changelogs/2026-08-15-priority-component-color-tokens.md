# Priority Component Color Tokens

## Summary

将实时请求流、系统状态指示器与应用 shell 的硬编码颜色迁移到现有主题 token，保持
daylight/night 下的状态语义一致。

## Changes

- `LiveRequestStreamV2` 的请求类型、探测、筛选、连接、按钮和 Redis 告警改用语义 token。
- `SystemStatusIndicator` 的健康状态点由内联颜色值改为 CSS 状态类和主题 token。
- `AppTopbar`、`LifecycleShell` 与 `UserMenuDropdown` 使用统一阴影、危险色和主色前景 token。
- 新增范围化测试，确保上述组件不再包含硬编码 `#hex` 或 `rgb/rgba()` 颜色。

## Verification

- `pnpm exec vitest run src/components/color-token-compliance.test.ts`
- `pnpm exec vue-tsc --noEmit`
- `pnpm build`
- 245 browser-use daylight/night and mobile/desktop verification.

## Rollback

```bash
bash scripts/deploy-seamless.sh rollback 245
```

代码回滚可直接 revert 本次提交。
