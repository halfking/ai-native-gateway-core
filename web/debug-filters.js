// 诊断脚本：检查实时请求流筛选选项数据
// 在浏览器控制台中运行此脚本，查看筛选选项的数据来源

console.log('=== 实时请求流筛选诊断 ===\n');

// 1. 检查 liveStreamStore 的 snapshot
const liveStreamState = window.__VUE_DEVTOOLS_GLOBAL_HOOK__?.apps?.[0]?.config?.globalProperties?.$root;
if (!liveStreamState) {
  console.error('无法访问 Vue 应用状态，请确保页面已加载');
} else {
  console.log('✓ Vue 应用状态可访问\n');
}

// 2. 检查当前维度和 lanes
console.log('--- 当前维度信息 ---');
const currentDimension = document.querySelector('.control-btn--active')?.textContent?.trim() || '未知';
console.log('当前分组维度:', currentDimension);

// 3. 检查 localStorage 中的快照数据
const checkLocalStorage = () => {
  console.log('\n--- LocalStorage 检查 ---');
  const keys = Object.keys(localStorage).filter(k => k.includes('live') || k.includes('stream'));
  if (keys.length === 0) {
    console.log('没有找到相关的 localStorage 数据');
  } else {
    keys.forEach(key => {
      console.log(`${key}:`, localStorage.getItem(key)?.substring(0, 100));
    });
  }
};
checkLocalStorage();

// 4. 检查筛选按钮的状态
console.log('\n--- 筛选按钮状态 ---');
const filterButtons = ['模型', '供应商', '原厂', '客户端'];
filterButtons.forEach(name => {
  const btn = Array.from(document.querySelectorAll('.filter-btn'))
    .find(el => el.textContent.includes(name));
  if (btn) {
    const badge = btn.querySelector('.filter-badge');
    const count = badge ? badge.textContent : '0';
    console.log(`${name}: 已选 ${count} 项`);
  }
});

// 5. 模拟点击并检查弹窗数据
console.log('\n--- 建议的诊断步骤 ---');
console.log('1. 打开 Vue DevTools');
console.log('2. 在 Console 中输入: $vm0.availableModels (如果选中了筛选组件)');
console.log('3. 检查 liveStreamStore.snapshot.dimensions 的内容');
console.log('4. 检查每个 lane.requests 数组中 tile 的字段是否完整');
console.log('\n--- 快速检查命令 ---');
console.log('在 Vue DevTools Console 中运行:');
console.log('  $vm0.$root.$children[0].snapshot');
console.log('  $vm0.$root.$children[0].lanes');
console.log('  $vm0.$root.$children[0].filterSourceLanes');

// 6. 创建一个全局辅助函数
window.debugLiveStreamFilters = () => {
  console.log('\n=== 详细诊断 ===');
  
  // 尝试访问 window.__LIVE_STREAM_STATE__（如果开发模式下暴露了）
  const snapshot = window.__LIVE_STREAM_STATE__?.snapshot;
  if (snapshot) {
    console.log('\n当前 snapshot:');
    console.log('- Summary:', snapshot.summary);
    console.log('- Dimensions:', Object.keys(snapshot.dimensions));
    
    for (const [dim, lanes] of Object.entries(snapshot.dimensions)) {
      console.log(`\n${dim} 维度 (${lanes.length} 个泳道):`);
      lanes.forEach((lane, idx) => {
        console.log(`  泳道 ${idx + 1}: ${lane.name} (${lane.requests.length} 个请求)`);
        if (lane.requests.length > 0) {
          const firstReq = lane.requests[0];
          console.log(`    首个请求字段:`, {
            model: firstReq.model,
            vendor: firstReq.vendor,
            provider: firstReq.provider,
            status: firstReq.status
          });
        }
      });
    }
    
    // 统计各字段的唯一值数量
    console.log('\n--- 可选项统计 ---');
    const allRequests = [];
    for (const lanes of Object.values(snapshot.dimensions)) {
      for (const lane of lanes) {
        allRequests.push(...lane.requests);
      }
    }
    
    const models = new Set(allRequests.map(r => r.model).filter(Boolean));
    const vendors = new Set(allRequests.map(r => r.vendor).filter(Boolean));
    const providers = new Set(allRequests.map(r => r.provider).filter(Boolean));
    
    console.log(`模型数量: ${models.size}`);
    console.log(`原厂数量: ${vendors.size}`);
    console.log(`供应商数量: ${providers.size}`);
    console.log(`总请求数: ${allRequests.length}`);
    
    if (models.size > 0) console.log('模型列表:', Array.from(models));
    if (vendors.size > 0) console.log('原厂列表:', Array.from(vendors));
    if (providers.size > 0) console.log('供应商列表:', Array.from(providers));
    
  } else {
    console.error('无法访问 snapshot 数据');
    console.log('请在开发模式下运行，或检查应用是否正确初始化');
  }
};

console.log('\n=== 诊断函数已创建 ===');
console.log('运行 window.debugLiveStreamFilters() 查看详细信息');
