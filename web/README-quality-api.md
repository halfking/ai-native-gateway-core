# 前端依赖安装说明

## 安装 axios

质量画像 API 客户端依赖 axios，需要先安装：

```bash
cd web
pnpm add axios
# 或
npm install axios
```

## 类型检查

安装依赖后，运行类型检查：

```bash
cd web
npx vue-tsc --noEmit
```

## 注意事项

- `web/src/types/quality-api.ts` - 不依赖外部库，可直接使用
- `web/src/api/quality-api-client.ts` - 需要 axios 依赖
- 如果暂不使用客户端，可以只导入类型定义

## 最小使用（无需 axios）

```typescript
// 只使用类型定义，不依赖 axios
import type { 
  ModelQualityProfile, 
  RankingItem 
} from '@/types/quality-api';

// 使用原生 fetch
async function getProviderQuality(providerId: number) {
  const response = await fetch(`/api/quality/providers/${providerId}`);
  const data: ProviderQualityResponse = await response.json();
  return data;
}
```
