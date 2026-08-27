<script setup lang="ts">
// 2026-08-27: /maintain/* 只应由 ai-native-maintain SPA 或 Go 的
// MaintainStaticHandler 服务。当浏览器在 /maintain/* 路径上加载出了
// Gateway 自己的 SPA（nginx try_files / 静态兜底返回了 Gateway 的
// index.html），说明维护后台未部署或未启动。历史上这里的 catch-all
// 把 /maintain/* 弹回 /，HomeView 又把未登录用户整页跳回 /maintain/home，
// 两端互相踢形成无限刷新闪动。此视图就地报错，切断循环。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElButton, ElCard } from 'element-plus'
import { probeMaintainAvailable } from '../config/edition'
import PublicPortalLayout from '../components/PublicPortalLayout.vue'

const router = useRouter()
const checking = ref(false)

function goConsole() {
  router.push('/')
}

// 重试：强制重新探测（绕过缓存），探测通过说明 maintain 已恢复，
// 整页重载后 nginx/Go 会返回真正的 maintain SPA。
async function retry() {
  checking.value = true
  const ok = await probeMaintainAvailable(true)
  checking.value = false
  if (ok && typeof window !== 'undefined') {
    window.location.reload()
  }
}

onMounted(() => {
  // 页面加载时顺手刷新一次缓存，避免停留在过期的 unavailable 状态。
  void probeMaintainAvailable(true)
})
</script>

<template>
  <PublicPortalLayout
    title="运维平台不可用"
    subtitle="/maintain 页面由独立的维护后台（ai-native-maintain）提供，当前该服务未启动或未部署。"
    kicker="503"
  >
    <el-card shadow="never" class="pub-card maintain-card">
      <div class="maintain-icon" aria-hidden="true">🛠️</div>
      <p class="maintain-detail">
        请求未能到达维护后台，页面已停止自动跳转以避免闪烁。
        请确认 ai-native-maintain 服务已启动（或
        <code>MAINTAIN_WEB_DIST</code> / nginx <code>/maintain</code> 路由已配置）后重试。
      </p>
      <div class="actions">
        <el-button type="primary" :loading="checking" @click="retry">重新检测</el-button>
        <el-button @click="goConsole">前往控制台</el-button>
      </div>
    </el-card>
  </PublicPortalLayout>
</template>

<style scoped>
.maintain-card {
  text-align: center;
  padding: 24px 16px 32px;
}
.maintain-icon {
  font-size: 56px;
  margin-bottom: 16px;
}
.maintain-detail {
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.8;
  margin: 0 0 8px;
}
.maintain-detail code {
  background: var(--bg-card);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
}
.actions {
  display: flex;
  gap: 12px;
  justify-content: center;
  flex-wrap: wrap;
  margin-top: 8px;
}
</style>
