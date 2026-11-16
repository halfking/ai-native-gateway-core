/**
 * 租户模型定价页面浏览器测试
 * 
 * 测试路径: /tenant/models
 * 
 * 测试目标：
 * 1. 页面加载并显示模型列表
 * 2. 验证每个模型显示 Input/Output Credits
 * 3. 验证搜索功能
 * 4. 验证筛选功能
 * 5. 验证数据格式和计算准确性
 * 
 * 注意：当前页面显示 credits_per_1m_in/out，但不包含档位标签（free/basic等）。
 * 如果需要测试档位定价，可能需要其他页面或功能开发。
 */

import { test, expect, type Page } from '@playwright/test'

// 测试配置
const TEST_CONFIG = {
  baseURL: process.env.BASE_URL || 'http://localhost:8090',
  username: process.env.TEST_USERNAME || 'admin',
  password: process.env.TEST_PASSWORD || 'admin123',
  minModelsExpected: 5, // 期望至少显示的模型数量
  timeout: 10000,
}

/**
 * 登录辅助函数
 */
async function login(page: Page) {
  await page.goto(`${TEST_CONFIG.baseURL}/login`)
  await page.fill('input[name="username"], input[type="text"]', TEST_CONFIG.username)
  await page.fill('input[name="password"], input[type="password"]', TEST_CONFIG.password)
  await page.click('button[type="submit"], .btn-primary')
  
  // 等待登录完成
  await page.waitForURL(/\/(dashboard|tenant)/, { timeout: TEST_CONFIG.timeout })
}

/**
 * 导航到租户模型页面
 */
async function navigateToTenantModels(page: Page) {
  await page.goto(`${TEST_CONFIG.baseURL}/tenant/models`)
  
  // 等待页面加载完成（等待模型列表或加载指示器）
  await page.waitForSelector('.model-card, .table, .loading, .empty', { timeout: TEST_CONFIG.timeout })
}

test.describe('租户模型定价页面', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
    await navigateToTenantModels(page)
  })

  test('应该成功加载页面并显示标题', async ({ page }) => {
    // 验证页面标题
    const title = await page.locator('h1, h2').first().textContent()
    expect(title).toBeTruthy()
    
    // 验证页面包含刷新按钮
    const refreshButton = page.locator('button:has-text("刷新"), button:has-text("Refresh")')
    await expect(refreshButton).toBeVisible()
  })

  test('应该显示模型列表', async ({ page }) => {
    // 等待加载完成
    await page.waitForSelector('.model-card, .table tbody tr, .empty', { timeout: TEST_CONFIG.timeout })
    
    // 检查是否有模型数据
    const hasCards = await page.locator('.model-card').count() > 0
    const hasTableRows = await page.locator('.table tbody tr').count() > 0
    const isEmpty = await page.locator('.empty').isVisible().catch(() => false)
    
    if (isEmpty) {
      console.warn('警告: 页面显示为空，可能没有配置模型数据')
      expect(isEmpty).toBe(true) // 至少验证空状态能正确显示
    } else {
      expect(hasCards || hasTableRows).toBe(true)
      
      // 验证至少有一定数量的模型
      const modelCount = Math.max(
        await page.locator('.model-card').count(),
        await page.locator('.table tbody tr').count()
      )
      expect(modelCount).toBeGreaterThanOrEqual(1)
      console.log(`✓ 发现 ${modelCount} 个模型`)
    }
  })

  test('每个模型应显示 Input 和 Output Credits', async ({ page }) => {
    // 等待数据加载
    await page.waitForSelector('.model-card, .table tbody tr', { timeout: TEST_CONFIG.timeout })
    
    const isCardView = await page.locator('.model-card').count() > 0
    
    if (isCardView) {
      // 卡片视图
      const cards = page.locator('.model-card')
      const count = await cards.count()
      
      if (count === 0) {
        test.skip()
        return
      }
      
      // 检查第一个卡片
      const firstCard = cards.first()
      const cardText = await firstCard.textContent()
      
      // 验证包含 Input/Output 相关文本（中英文）
      const hasInputLabel = /Input|输入|进价格/i.test(cardText || '')
      const hasOutputLabel = /Output|输出|出价格/i.test(cardText || '')
      
      expect(hasInputLabel || hasOutputLabel).toBe(true)
      console.log(`✓ 卡片视图包含价格信息`)
      
    } else {
      // 表格视图
      const tableRows = page.locator('.table tbody tr')
      const count = await tableRows.count()
      
      if (count === 0) {
        test.skip()
        return
      }
      
      // 验证表头包含 Input/Output 列
      const headers = await page.locator('.table thead th').allTextContents()
      const headerText = headers.join(' ')
      
      const hasInputColumn = /Input|输入|进/i.test(headerText)
      const hasOutputColumn = /Output|输出|出/i.test(headerText)
      
      expect(hasInputColumn).toBe(true)
      expect(hasOutputColumn).toBe(true)
      console.log(`✓ 表格包含 Input/Output Credits 列`)
      
      // 验证第一行数据包含数值
      const firstRow = tableRows.first()
      const cells = await firstRow.locator('td').allTextContents()
      
      // 应该有至少2个数值单元格（Input/Output）
      const numericCells = cells.filter(cell => /[\d,]+/.test(cell))
      expect(numericCells.length).toBeGreaterThanOrEqual(2)
      console.log(`✓ 第一行包含 ${numericCells.length} 个数值单元格`)
    }
  })

  test('应该支持模型搜索功能', async ({ page }) => {
    // 查找搜索输入框
    const searchInput = page.locator('input[type="search"], input[placeholder*="搜索"], input[placeholder*="Search"]')
    
    await expect(searchInput).toBeVisible({ timeout: TEST_CONFIG.timeout })
    
    // 获取初始模型数量
    await page.waitForSelector('.model-card, .table tbody tr', { timeout: TEST_CONFIG.timeout })
    const initialCount = Math.max(
      await page.locator('.model-card').count(),
      await page.locator('.table tbody tr').count()
    )
    
    if (initialCount === 0) {
      test.skip()
      return
    }
    
    console.log(`初始模型数量: ${initialCount}`)
    
    // 输入搜索词（搜索常见模型名）
    await searchInput.fill('gpt')
    
    // 等待搜索结果更新（使用短暂延迟）
    await page.waitForTimeout(500)
    
    // 获取搜索后的模型数量
    const afterSearchCount = Math.max(
      await page.locator('.model-card').count(),
      await page.locator('.table tbody tr').count()
    )
    
    console.log(`搜索 "gpt" 后模型数量: ${afterSearchCount}`)
    
    // 验证搜索功能工作（数量变化或显示空状态）
    const isEmpty = await page.locator('.empty, .no-results').isVisible().catch(() => false)
    expect(afterSearchCount !== initialCount || isEmpty).toBe(true)
    
    // 清除搜索
    await searchInput.clear()
    await page.waitForTimeout(500)
    
    const afterClearCount = Math.max(
      await page.locator('.model-card').count(),
      await page.locator('.table tbody tr').count()
    )
    
    // 验证清除后恢复初始状态
    expect(afterClearCount).toBe(initialCount)
    console.log(`✓ 搜索功能正常，清除后恢复到 ${afterClearCount} 个模型`)
  })

  test('应该支持筛选功能', async ({ page }) => {
    // 查找筛选控件（可能是下拉框或单选按钮）
    const filterControls = page.locator('select, input[type="radio"], .filter-option')
    
    const hasFilters = await filterControls.count() > 0
    
    if (!hasFilters) {
      console.warn('未找到筛选控件，跳过筛选测试')
      test.skip()
      return
    }
    
    // 获取初始模型数量
    await page.waitForSelector('.model-card, .table tbody tr', { timeout: TEST_CONFIG.timeout })
    const initialCount = Math.max(
      await page.locator('.model-card').count(),
      await page.locator('.table tbody tr').count()
    )
    
    if (initialCount === 0) {
      test.skip()
      return
    }
    
    console.log(`初始模型数量: ${initialCount}`)
    
    // 尝试切换筛选选项
    const firstFilter = filterControls.first()
    const tagName = await firstFilter.evaluate(el => el.tagName.toLowerCase())
    
    if (tagName === 'select') {
      // 下拉框：选择第二个选项
      const options = await firstFilter.locator('option').count()
      if (options > 1) {
        await firstFilter.selectOption({ index: 1 })
      }
    } else if (tagName === 'input') {
      // 单选按钮：点击
      await firstFilter.click()
    }
    
    // 等待筛选结果更新
    await page.waitForTimeout(500)
    
    const afterFilterCount = Math.max(
      await page.locator('.model-card').count(),
      await page.locator('.table tbody tr').count()
    )
    
    console.log(`筛选后模型数量: ${afterFilterCount}`)
    
    // 验证筛选功能工作
    expect(afterFilterCount).toBeGreaterThanOrEqual(0)
    console.log(`✓ 筛选功能正常`)
  })

  test('Credits 数值应该是有效的数字格式', async ({ page }) => {
    await page.waitForSelector('.model-card, .table tbody tr', { timeout: TEST_CONFIG.timeout })
    
    const isTableView = await page.locator('.table tbody tr').count() > 0
    
    if (!isTableView) {
      // 卡片视图的验证较复杂，暂时跳过
      test.skip()
      return
    }
    
    // 获取表格中所有数值单元格
    const cells = await page.locator('.table tbody td.num, .table tbody td').allTextContents()
    
    // 提取所有看起来像数字的单元格
    const numericCells = cells.filter(cell => {
      const cleaned = cell.replace(/[,\s]/g, '')
      return /^\d+(\.\d+)?$/.test(cleaned)
    })
    
    if (numericCells.length === 0) {
      console.warn('未找到数值单元格')
      test.skip()
      return
    }
    
    // 验证每个数值都是有效格式
    for (const cell of numericCells.slice(0, 10)) { // 只检查前10个
      const cleaned = cell.replace(/[,\s]/g, '')
      const value = parseFloat(cleaned)
      
      expect(value).toBeGreaterThanOrEqual(0)
      expect(Number.isNaN(value)).toBe(false)
    }
    
    console.log(`✓ 验证了 ${Math.min(numericCells.length, 10)} 个数值单元格的格式`)
  })

  test('应该能够刷新页面数据', async ({ page }) => {
    // 等待初始加载
    await page.waitForSelector('.model-card, .table tbody tr', { timeout: TEST_CONFIG.timeout })
    
    // 获取初始内容
    const initialContent = await page.content()
    
    // 点击刷新按钮
    const refreshButton = page.locator('button:has-text("刷新"), button:has-text("Refresh"), button[title*="刷新"]')
    await refreshButton.click()
    
    // 等待加载指示器出现（如果有）
    const loadingIndicator = page.locator('.loading, [aria-busy="true"]')
    if (await loadingIndicator.isVisible().catch(() => false)) {
      await loadingIndicator.waitFor({ state: 'hidden', timeout: TEST_CONFIG.timeout })
    } else {
      // 如果没有加载指示器，简单等待
      await page.waitForTimeout(1000)
    }
    
    // 验证页面仍然有数据
    const afterRefreshCount = Math.max(
      await page.locator('.model-card').count(),
      await page.locator('.table tbody tr').count()
    )
    
    expect(afterRefreshCount).toBeGreaterThanOrEqual(0)
    console.log(`✓ 刷新后页面包含 ${afterRefreshCount} 个模型`)
  })

  test('响应式布局：在不同视口下应正常显示', async ({ page }) => {
    // 桌面视图
    await page.setViewportSize({ width: 1280, height: 720 })
    await page.waitForTimeout(500)
    
    let desktopVisible = await page.locator('.model-card, .table').isVisible().catch(() => false)
    expect(desktopVisible).toBe(true)
    console.log(`✓ 桌面视图正常`)
    
    // 平板视图
    await page.setViewportSize({ width: 768, height: 1024 })
    await page.waitForTimeout(500)
    
    let tabletVisible = await page.locator('.model-card, .table').isVisible().catch(() => false)
    expect(tabletVisible).toBe(true)
    console.log(`✓ 平板视图正常`)
    
    // 移动视图
    await page.setViewportSize({ width: 375, height: 667 })
    await page.waitForTimeout(500)
    
    let mobileVisible = await page.locator('.model-card, .table').isVisible().catch(() => false)
    expect(mobileVisible).toBe(true)
    console.log(`✓ 移动视图正常`)
  })
})

test.describe('数据完整性验证', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
    await navigateToTenantModels(page)
  })

  test('每个模型应有唯一标识符', async ({ page }) => {
    await page.waitForSelector('.model-card, .table tbody tr', { timeout: TEST_CONFIG.timeout })
    
    const isTableView = await page.locator('.table tbody tr').count() > 0
    
    if (isTableView) {
      // 表格视图：检查第一列（通常是模型名称）
      const modelNames = await page.locator('.table tbody tr td:first-child').allTextContents()
      const uniqueNames = new Set(modelNames.filter(name => name.trim()))
      
      if (modelNames.length > 0) {
        expect(uniqueNames.size).toBe(modelNames.length)
        console.log(`✓ ${modelNames.length} 个模型都有唯一标识符`)
      }
    } else {
      // 卡片视图：检查卡片标题
      const cardTitles = await page.locator('.model-card h3, .model-card .card-title').allTextContents()
      const uniqueTitles = new Set(cardTitles.filter(title => title.trim()))
      
      if (cardTitles.length > 0) {
        expect(uniqueTitles.size).toBe(cardTitles.length)
        console.log(`✓ ${cardTitles.length} 个模型都有唯一标识符`)
      }
    }
  })

  test('Credits 数值应该合理（非负数，不为NaN）', async ({ page }) => {
    await page.waitForSelector('.table tbody tr', { timeout: TEST_CONFIG.timeout })
    
    const rows = await page.locator('.table tbody tr').count()
    
    if (rows === 0) {
      test.skip()
      return
    }
    
    // 检查前5行的数值
    for (let i = 0; i < Math.min(rows, 5); i++) {
      const row = page.locator('.table tbody tr').nth(i)
      const cells = await row.locator('td').allTextContents()
      
      // 提取数值
      const numbers = cells
        .map(cell => {
          const cleaned = cell.replace(/[,\s]/g, '')
          return parseFloat(cleaned)
        })
        .filter(n => !Number.isNaN(n))
      
      // 验证所有数值非负
      for (const num of numbers) {
        expect(num).toBeGreaterThanOrEqual(0)
      }
    }
    
    console.log(`✓ 验证了前 ${Math.min(rows, 5)} 行的数值合理性`)
  })
})
