<script setup lang="ts">
// SalesWorkbenchView.vue — 报价系统 MVP 工作台（2026-11-05 W1-T2 前端轮；
// 2026-11-08 风暴档审计轮扩 W2：管理层看板页签 + 成本漂移徽标 + 报价弹窗
// 模型选择（服务端派生成本后的正确形状）+ 三档快填 + 收口弹窗化 + 分享撤销）。
// 四页签：供给池（sellable+登记）/ 需求池 / 报价单（版本+审批+分享）/ 看板(super)。
// 后端：admin/sales_handlers.go + sales 包（942–949）。
//
// 纪律（2026-11-04 实施规划 §6 W1-T2 验收门 + 2026-11-08 审计）：
//  - withheld 组单独呈现（同名多价整组扣下，不进可售清单）；
//  - C3 置信档逐行展示（trusted 是出正式单的准入，前端不阻断但如实标色）；
//  - 分享 token 只在创建响应出现一次，复制后不可再取（明文不落库）；
//  - 行成本/币种/置信一律服务端派生（P1/P6 修复）：报价行从可售清单选模型，
//    手填 canonical 已不可能通过后端校验 ⇒ 弹窗内只有模型选择器；
//  - 实收毛利一栏显式标「待租户计费激活」（风暴档 §3.6 诚实边界）。
import { computed, onMounted, ref } from 'vue'
import {
  ElAlert, ElButton, ElDialog, ElForm, ElFormItem, ElInput,
  ElInputNumber, ElMessage, ElOption, ElSelect, ElTable, ElTableColumn,
  ElTabPane, ElTabs, ElTag,
} from 'element-plus'
import { useI18n } from 'vue-i18n'
import { isSuperAdmin } from '../../store'
import {
  closeDemandCard, claimDemandCard, createDemandCard, listDemandCards,
  closeQuote, createQuote, createQuoteShare, decideQuote, getSalesDashboard,
  listQuoteApprovals, listQuotes, listQuoteShares, listQuoteVersions,
  listSupplyAnnotations, getSalesSellable, revokeQuoteShare,
  sendQuote, submitQuote, upsertSupplyAnnotation,
  type DemandCard, type Quote, type QuoteApproval, type QuoteVersion,
  type SalesDashboard, type SellableList, type SellableRow, type ShareLink,
} from '../../api/sales'

const { t } = useI18n()

// 写权限应用层闸（2026-11-06 审计 P-5）：decide 与供给登记写后端是
// super-only（admin/sales_handlers.go decide 内判 salesAuthRole、
// POST supply = h.superAdmin）⇒ 前端对非 super 隐藏对应写按钮（读侧
// 与其余 h.admin 动作不收窄），避免「必 403 的按钮」。
// 2026-11-08：看板端点同样是 super-only（全团队聚合毛利 ⇒ 管理层面）。
const canDecide = isSuperAdmin()

type WorkbenchTab = 'supply' | 'demand' | 'quotes' | 'dashboard'
const tab = ref<WorkbenchTab>('supply')
const loading = ref(false)
const errorText = ref('')
// reload 无序守卫（P-11）：快速切页签时旧响应后到不得覆盖新页签状态。
let reloadSeq = 0

// ── 供给池 ──
const sellable = ref<SellableList | null>(null)
const sellableQuery = ref('')
const filteredSellable = computed(() => {
  const q = sellableQuery.value.trim().toLowerCase()
  if (!sellable.value) return []
  if (!q) return sellable.value.items
  return sellable.value.items.filter(
    (r) => r.canonical_name.toLowerCase().includes(q) || r.raw_model_name.toLowerCase().includes(q),
  )
})

// ── 需求池 ──
const demandCards = ref<DemandCard[]>([])
const demandFilter = ref('')
const demandForm = ref({ customer_name: '', scenario: '', monthly_usage: '', budget: '', window_hint: '', notes: '' })

// ── 报价单 ──
const quotes = ref<Quote[]>([])
const quoteFilter = ref('')
const quoteDialogVisible = ref(false)
// 报价行：模型从可售清单选（服务端只认视图内 canonical）；成本是展示位
// （服务端派生，只读），销售填的是量/零售价/折扣。
interface QuoteDraftItem {
  canonical_name: string
  display_name: string
  cost_in: number | null
  cost_out: number | null
  cost_currency: string
  est_tokens_in: number
  est_tokens_out: number
  rate_in: number | null
  rate_out: number | null
  discount_pct: number
}
const quoteForm = ref({ customer_name: '', title: '', tier: 'standard' })
const quoteItems = ref<QuoteDraftItem[]>([])
const versions = ref<QuoteVersion[]>([])
const versionsQuoteId = ref<number | null>(null)
const shareLinks = ref<ShareLink[]>([])
const sharesQuoteId = ref<number | null>(null)
const lastShareToken = ref('')
const shareForm = ref({ contact_name: '', contact_email: '' })
const annotateDialogVisible = ref(false)
const versionsDialogVisible = ref(false)
const sharesDialogVisible = ref(false)

// ── 看板（W2-T2，super） ──
const dashboard = ref<SalesDashboard | null>(null)

async function reload() {
  const seq = ++reloadSeq
  loading.value = true
  errorText.value = ''
  try {
    if (tab.value === 'supply') {
      const r = await getSalesSellable()
      if (seq !== reloadSeq) return
      sellable.value = r
    } else if (tab.value === 'demand') {
      const r = (await listDemandCards()).items
      if (seq !== reloadSeq) return
      demandCards.value = r
    } else if (tab.value === 'dashboard') {
      const r = await getSalesDashboard()
      if (seq !== reloadSeq) return
      dashboard.value = r
    } else {
      const r = (await listQuotes()).items
      if (seq !== reloadSeq) return
      quotes.value = r
    }
  } catch (e) {
    if (seq !== reloadSeq) return
    errorText.value = String((e as Error).message || e)
  } finally {
    if (seq === reloadSeq) loading.value = false
  }
}

async function switchTab(name: string | number) {
  tab.value = name as WorkbenchTab
  await reload()
}

const funnelOrder = ['draft', 'pending_approval', 'approved', 'sent', 'won', 'lost', 'expired'] as const

// ── 供给登记（ops 面；最简形态：对选中行补 SLA 文案与联系人） ──
const annotateTarget = ref<{ credential_id: number; provider_model_id: number; name: string } | null>(null)
const annotateForm = ref({ sla_tier: '', vendor_contact: '', capacity_notes: '', status: 'active' })

function openAnnotate(row: { credential_id: number; provider_model_id: number; raw_model_name: string }) {
  annotateTarget.value = {
    credential_id: row.credential_id,
    provider_model_id: row.provider_model_id,
    name: row.raw_model_name,
  }
  annotateForm.value = { sla_tier: '', vendor_contact: '', capacity_notes: '', status: 'active' }
  annotateDialogVisible.value = true
}

async function saveAnnotation() {
  if (!annotateTarget.value) return
  try {
    await upsertSupplyAnnotation({
      credential_id: annotateTarget.value.credential_id,
      provider_model_id: annotateTarget.value.provider_model_id,
      sla_tier: annotateForm.value.sla_tier,
      vendor_contact: annotateForm.value.vendor_contact,
      capacity_notes: annotateForm.value.capacity_notes,
      status: annotateForm.value.status,
    })
    ElMessage.success(t('sales.saved'))
    annotateDialogVisible.value = false
    annotateTarget.value = null
    void listSupplyAnnotations() // 保持登记清单缓存面（不阻塞主流程）
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

// ── 需求池动作 ──
async function createCard() {
  if (!demandForm.value.customer_name.trim()) {
    ElMessage.warning(t('sales.customerRequired'))
    return
  }
  try {
    await createDemandCard({ ...demandForm.value })
    demandForm.value = { customer_name: '', scenario: '', monthly_usage: '', budget: '', window_hint: '', notes: '' }
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function claim(card: DemandCard) {
  try {
    await claimDemandCard(card.id)
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

const closeTarget = ref<DemandCard | null>(null)
const closeForm = ref({ status: 'won', reason: '' })
const closeDialogVisible = ref(false)

async function saveClose() {
  if (!closeTarget.value) return
  try {
    await closeDemandCard(closeTarget.value.id, closeForm.value.status, closeForm.value.reason)
    closeDialogVisible.value = false
    closeTarget.value = null
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

// ── 报价动作 ──
async function openQuoteDialog() {
  if (!sellable.value) {
    try {
      sellable.value = await getSalesSellable()
    } catch (e) {
      ElMessage.error(String((e as Error).message || e))
      return
    }
  }
  quoteForm.value = { customer_name: '', title: '', tier: 'standard' }
  quoteItems.value = []
  quoteDialogVisible.value = true
}

// 模型从可售清单选（2026-11-08 P1/P6：服务端只认视图内 canonical，成本
// 服务端派生 —— 前端只带商业轴进 payload，成本列展示派生参考价）。
function addQuoteItem(row: SellableRow) {
  if (quoteItems.value.some((it) => it.canonical_name === row.canonical_name)) {
    ElMessage.warning(t('sales.itemExists'))
    return
  }
  quoteItems.value.push({
    canonical_name: row.canonical_name,
    display_name: row.raw_model_name,
    cost_in: row.supplier_in_per_1m != null ? Number(row.supplier_in_per_1m) : null,
    cost_out: row.supplier_out_per_1m != null ? Number(row.supplier_out_per_1m) : null,
    cost_currency: row.supplier_currency,
    est_tokens_in: 0,
    est_tokens_out: 0,
    rate_in: null,
    rate_out: null,
    discount_pct: 0,
  })
}

function removeQuoteItem(idx: number) {
  quoteItems.value.splice(idx, 1)
}

// 三档快填（风暴档 §8-5）：零售价 = 派生成本 × 档位系数（系数来自
// sellable.tier_coefficients，settings 热改即生效）。基础价轴之外的商业
// 折扣不动；已手改过价的行也整体重算（快填语义）。
function applyTierCoef(tier: string) {
  const coefs = sellable.value?.tier_coefficients || {}
  const coef = coefs[tier]
  if (!coef || coef <= 0) return
  quoteForm.value.tier = tier
  for (const it of quoteItems.value) {
    if (it.cost_in != null) it.rate_in = Math.ceil(it.cost_in * coef * 100) / 100
    if (it.cost_out != null) it.rate_out = Math.ceil(it.cost_out * coef * 100) / 100
  }
}

async function createNewQuote() {
  if (!quoteForm.value.customer_name.trim() || quoteItems.value.length === 0) {
    ElMessage.warning(t('sales.quoteDraftIncomplete'))
    return
  }
  try {
    await createQuote({
      customer_name: quoteForm.value.customer_name,
      title: quoteForm.value.title,
      tier: quoteForm.value.tier,
      items: quoteItems.value.map((it) => ({
        canonical_name: it.canonical_name,
        display_name: it.display_name,
        est_tokens_in: it.est_tokens_in,
        est_tokens_out: it.est_tokens_out,
        rate_in: it.rate_in,
        rate_out: it.rate_out,
        discount_pct: it.discount_pct,
      })),
    })
    quoteDialogVisible.value = false
    quoteForm.value = { customer_name: '', title: '', tier: 'standard' }
    quoteItems.value = []
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

// 收口弹窗化（2026-11-08 前端审计 P3：window.prompt 与需求池 el-dialog
// 形态不一致，且原生 prompt 在部分 webview 被静默拦截）。
const quoteCloseTarget = ref<Quote | null>(null)
const quoteCloseForm = ref({ status: 'won' as 'won' | 'lost', reason: '' })
const quoteCloseDialogVisible = ref(false)

async function act(q: Quote, action: 'submit' | 'send' | 'won' | 'lost') {
  try {
    if (action === 'submit') {
      await submitQuote(q.id)
    } else if (action === 'send') {
      await sendQuote(q.id)
    } else {
      quoteCloseTarget.value = q
      quoteCloseForm.value = { status: action, reason: '' }
      quoteCloseDialogVisible.value = true
      return
    }
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function saveQuoteClose() {
  if (!quoteCloseTarget.value) return
  if (!quoteCloseForm.value.reason.trim()) {
    ElMessage.warning(t('sales.reasonRequired'))
    return
  }
  try {
    await closeQuote(quoteCloseTarget.value.id, quoteCloseForm.value.status, quoteCloseForm.value.reason.trim())
    quoteCloseDialogVisible.value = false
    quoteCloseTarget.value = null
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function decide(q: Quote, decision: 'approved' | 'rejected') {
  try {
    await decideQuote(q.id, decision)
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function showVersions(q: Quote) {
  try {
    versions.value = (await listQuoteVersions(q.id)).items
    versionsQuoteId.value = q.id
    versionsDialogVisible.value = true
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function openShares(q: Quote) {
  try {
    shareLinks.value = (await listQuoteShares(q.id)).items
    lastShareToken.value = ''
    shareForm.value = { contact_name: '', contact_email: '' }
    sharesQuoteId.value = q.id
    sharesDialogVisible.value = true
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function mintShare(quoteId: number) {
  if (!shareForm.value.contact_name.trim()) {
    ElMessage.warning(t('sales.contactRequired'))
    return
  }
  try {
    const link = await createQuoteShare(quoteId, {
      contact_name: shareForm.value.contact_name,
      contact_email: shareForm.value.contact_email,
      ttl_hours: 24 * 14,
    })
    // token 只在创建响应出现一次（明文不落库）⇒ 立刻呈现并复制。
    lastShareToken.value = link.token || ''
    if (lastShareToken.value) {
      try {
        await navigator.clipboard.writeText(`${window.location.origin}/public/quote/${lastShareToken.value}`)
        ElMessage.success(t('sales.shareTokenCopied'))
      } catch {
        ElMessage.info(t('sales.shareTokenShown'))
      }
    }
    shareLinks.value = (await listQuoteShares(quoteId)).items
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

// 撤销分享（2026-11-08 审计缺口收口：后端此前无撤销 API 面）。
async function revokeShare(share: ShareLink) {
  if (sharesQuoteId.value === null) return
  try {
    await revokeQuoteShare(sharesQuoteId.value, share.id)
    shareLinks.value = (await listQuoteShares(sharesQuoteId.value)).items
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

// ── 审批记录（2026-11-06 审计 P-4）：trigger_reasons 是 submit 内服务端
// 红线裁决（决议 B）的可复算快照，此前取回后零渲染 ⇒ 审批人看不到触发
// 维度/值/阈值。这里接现成 /approvals 端点做只读呈现。
const approvals = ref<QuoteApproval[]>([])
const approvalsDialogVisible = ref(false)
const approvalsQuoteId = ref<number | null>(null)

function formatTriggerReasons(raw: string): string {
  try {
    const parsed = JSON.parse(raw)
    if (Array.isArray(parsed)) return parsed.map(String).join('；')
    if (parsed && typeof parsed === 'object') return Object.entries(parsed).map(([k, v]) => `${k}=${String(v)}`).join('；')
  } catch { /* 非 JSON：原样呈现 */ }
  return raw
}

async function showApprovals(q: Quote) {
  try {
    approvals.value = (await listQuoteApprovals(q.id)).items
    approvalsQuoteId.value = q.id
    approvalsDialogVisible.value = true
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

onMounted(reload)
</script>

<template>
  <div class="sales-workbench">
    <el-tabs :model-value="tab" @tab-change="switchTab">
      <el-tab-pane :label="t('sales.tabSupply')" name="supply" />
      <el-tab-pane :label="t('sales.tabDemand')" name="demand" />
      <el-tab-pane :label="t('sales.tabQuotes')" name="quotes" />
      <el-tab-pane v-if="canDecide" :label="t('sales.tabDashboard')" name="dashboard" />
    </el-tabs>

    <el-alert v-if="errorText" :title="errorText" type="error" show-icon :closable="false" class="block" />

    <!-- ── 供给池 ─────────────────────────────────────────────────── -->
    <div v-if="tab === 'supply'">
      <el-input
        v-model="sellableQuery"
        :placeholder="t('sales.searchPlaceholder')"
        clearable
        class="block narrow"
      />
      <el-alert
        v-if="sellable && sellable.withheld_count > 0"
        :title="t('sales.withheldNotice', { count: sellable.withheld_count })"
        type="warning"
        show-icon
        :closable="false"
        class="block"
      />
      <!-- withheld 明细（P-9）：整组扣下的模型/原因/价格变体，运营据此人工排查 -->
      <el-table
        v-if="sellable && sellable.withheld.length"
        :data="sellable.withheld"
        size="small"
        class="block withheld-table"
      >
        <el-table-column prop="canonical_name" :label="t('sales.colModel')" min-width="160" />
        <el-table-column prop="reason" :label="t('sales.withheldReason')" min-width="160" />
        <el-table-column :label="t('sales.withheldVariants')" min-width="200">
          <template #default="{ row }">
            {{ (row.price_variants || []).map((v: [string | null, string | null]) => `${v[0] ?? '—'} / ${v[1] ?? '—'}`).join('；') }}
          </template>
        </el-table-column>
      </el-table>
      <el-table v-loading="loading" :data="filteredSellable" size="small" class="block sellable-table">
        <el-table-column prop="canonical_name" :label="t('sales.colModel')" min-width="180" />
        <el-table-column prop="provider_name" :label="t('sales.colProvider')" width="120" />
        <el-table-column prop="billing_mode" :label="t('sales.colBilling')" width="110" />
        <el-table-column :label="t('sales.colSupplierPrice')" width="150">
          <template #default="{ row }">
            {{ row.supplier_in_per_1m }} / {{ row.supplier_out_per_1m }} {{ row.supplier_currency }}
          </template>
        </el-table-column>
        <el-table-column prop="market_position" :label="t('sales.colMarketPosition')" width="120" />
        <el-table-column prop="price_confidence" :label="t('sales.colConfidence')" width="140">
          <template #default="{ row }">
            <el-tag :type="row.price_confidence === 'trusted' ? 'success' : 'warning'" size="small">
              {{ row.price_confidence }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('sales.colSla')" min-width="120">
          <template #default="{ row }">
            {{ row.annotation?.sla_tier || '—' }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sales.colActions')" width="100" fixed="right">
          <template #default="{ row }">
            <el-button v-if="canDecide" size="small" @click="openAnnotate(row as unknown as { credential_id: number; provider_model_id: number; raw_model_name: string })">{{ t('sales.annotate') }}</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-dialog v-model="annotateDialogVisible" :title="t('sales.annotateTitle', { name: annotateTarget?.name || '' })" width="480px" class="sales-dialog">
        <el-form label-width="110px">
          <el-form-item :label="t('sales.slaTier')">
            <el-input v-model="annotateForm.sla_tier" />
          </el-form-item>
          <el-form-item :label="t('sales.vendorContact')">
            <el-input v-model="annotateForm.vendor_contact" />
          </el-form-item>
          <el-form-item :label="t('sales.capacity')">
            <el-input v-model="annotateForm.capacity_notes" type="textarea" :rows="2" />
          </el-form-item>
          <el-form-item :label="t('sales.status')">
            <el-select v-model="annotateForm.status">
              <el-option label="active" value="active" />
              <el-option label="suspended" value="suspended" />
            </el-select>
          </el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="annotateDialogVisible = false">{{ t('common.cancel') }}</el-button>
          <el-button type="primary" @click="saveAnnotation">{{ t('common.save') }}</el-button>
        </template>
      </el-dialog>
    </div>

    <!-- ── 需求池 ─────────────────────────────────────────────────── -->
    <div v-else-if="tab === 'demand'">
      <el-form inline class="block">
        <el-form-item :label="t('sales.customer')">
          <el-input v-model="demandForm.customer_name" style="width: 160px" />
        </el-form-item>
        <el-form-item :label="t('sales.scenario')">
          <el-input v-model="demandForm.scenario" style="width: 180px" />
        </el-form-item>
        <el-form-item :label="t('sales.monthlyUsage')">
          <el-input v-model="demandForm.monthly_usage" style="width: 160px" />
        </el-form-item>
        <el-form-item :label="t('sales.budget')">
          <el-input v-model="demandForm.budget" style="width: 120px" />
        </el-form-item>
        <el-form-item :label="t('sales.windowHint')">
          <el-input v-model="demandForm.window_hint" style="width: 140px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="createCard">{{ t('sales.createCard') }}</el-button>
        </el-form-item>
      </el-form>

      <el-select v-model="demandFilter" :placeholder="t('sales.statusFilter')" size="small" clearable class="block narrow" @change="reload">
        <el-option v-for="s in ['open', 'claimed', 'quoted', 'won', 'lost', 'closed']" :key="s" :label="s" :value="s" />
      </el-select>

      <el-table v-loading="loading" :data="demandCards" size="small" class="block">
        <el-table-column prop="customer_name" :label="t('sales.customer')" min-width="140" />
        <el-table-column prop="scenario" :label="t('sales.scenario')" min-width="140" />
        <el-table-column prop="monthly_usage" :label="t('sales.monthlyUsage')" min-width="130" />
        <el-table-column prop="budget" :label="t('sales.budget')" width="100" />
        <el-table-column :label="t('sales.colStatus')" width="110">
          <template #default="{ row }">
            <el-tag :type="row.expired ? 'danger' : row.status === 'won' ? 'success' : 'info'" size="small">
              {{ row.expired ? t('sales.expired') : row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="claimed_by" :label="t('sales.claimedBy')" width="110" />
        <el-table-column :label="t('sales.colActions')" width="160" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.status === 'open'" size="small" @click="claim(row as unknown as DemandCard)">{{ t('sales.claim') }}</el-button>
            <el-button v-if="['claimed', 'quoted'].includes(row.status)" size="small" @click="closeTarget = row as unknown as DemandCard; closeForm = { status: 'won', reason: '' }; closeDialogVisible = true">
              {{ t('sales.close') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-dialog v-model="closeDialogVisible" :title="t('sales.closeTitle')" width="420px" class="sales-dialog">
        <el-form label-width="90px">
          <el-form-item :label="t('sales.colStatus')">
            <el-select v-model="closeForm.status">
              <el-option label="won" value="won" />
              <el-option label="lost" value="lost" />
              <el-option label="closed" value="closed" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('sales.reason')">
            <el-input v-model="closeForm.reason" type="textarea" :rows="2" :placeholder="t('sales.reasonRequired')" />
          </el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="closeDialogVisible = false">{{ t('common.cancel') }}</el-button>
          <el-button type="primary" @click="saveClose">{{ t('common.save') }}</el-button>
        </template>
      </el-dialog>
    </div>

    <!-- ── 报价单 ─────────────────────────────────────────────────── -->
    <div v-else-if="tab === 'quotes'">
      <el-button type="primary" class="block" @click="openQuoteDialog">{{ t('sales.createQuote') }}</el-button>

      <el-table v-loading="loading" :data="quotes" size="small" class="block">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column prop="customer_name" :label="t('sales.customer')" min-width="130" />
        <el-table-column prop="title" :label="t('sales.quoteTitle')" min-width="130" />
        <el-table-column :label="t('sales.colStatus')" width="150">
          <template #default="{ row }">
            <el-tag :type="row.version?.expired ? 'danger' : row.status === 'won' ? 'success' : row.status === 'pending_approval' ? 'warning' : 'info'" size="small">
              {{ row.version?.expired ? t('sales.expired') : row.status }}
            </el-tag>
            <el-tag v-if="row.cost_drifted" type="warning" size="small" class="drift-tag">
              {{ t('sales.costDrifted') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('sales.colVersion')" width="90">
          <template #default="{ row }"> v{{ row.version?.version_no ?? '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('sales.colMargin')" width="110">
          <template #default="{ row }">
            {{ row.version?.margin_pct != null ? row.version.margin_pct.toFixed(1) + '%' : '—' }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sales.colActions')" width="330" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.status === 'draft'" size="small" type="warning" @click="act(row as unknown as Quote, 'submit')">{{ t('sales.submit') }}</el-button>
            <el-button v-if="canDecide && row.status === 'pending_approval'" size="small" type="success" @click="decide(row as unknown as Quote, 'approved')">{{ t('sales.approve') }}</el-button>
            <el-button v-if="canDecide && row.status === 'pending_approval'" size="small" type="danger" @click="decide(row as unknown as Quote, 'rejected')">{{ t('sales.reject') }}</el-button>
            <el-button v-if="row.status === 'approved'" size="small" type="primary" @click="act(row as unknown as Quote, 'send')">{{ t('sales.send') }}</el-button>
            <el-button v-if="row.status === 'sent'" size="small" type="success" @click="act(row as unknown as Quote, 'won')">{{ t('sales.markWon') }}</el-button>
            <el-button v-if="row.status === 'sent'" size="small" type="info" @click="act(row as unknown as Quote, 'lost')">{{ t('sales.markLost') }}</el-button>
            <el-button size="small" @click="showApprovals(row as unknown as Quote)">{{ t('sales.approvals') }}</el-button>
            <el-button size="small" @click="showVersions(row as unknown as Quote)">{{ t('sales.versions') }}</el-button>
            <el-button size="small" @click="openShares(row as unknown as Quote)">{{ t('sales.shares') }}</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- 建单弹窗（2026-11-08 P1/P6 形状：模型从可售清单选，成本只读展示，
           三档快填 = 派生成本 × settings 档位系数） -->
      <el-dialog v-model="quoteDialogVisible" :title="t('sales.createQuote')" width="860px" class="sales-dialog">
        <el-form inline>
          <el-form-item :label="t('sales.customer')">
            <el-input v-model="quoteForm.customer_name" style="width: 150px" />
          </el-form-item>
          <el-form-item :label="t('sales.quoteTitle')">
            <el-input v-model="quoteForm.title" style="width: 160px" />
          </el-form-item>
          <el-form-item :label="t('sales.tier')">
            <el-select v-model="quoteForm.tier" style="width: 130px">
              <el-option label="aggressive" value="aggressive" />
              <el-option label="standard" value="standard" />
              <el-option label="conservative" value="conservative" />
            </el-select>
          </el-form-item>
        </el-form>
        <div class="tier-quick">
          <span class="tier-quick-label">{{ t('sales.tierQuickFill') }}</span>
          <el-button size="small" @click="applyTierCoef('aggressive')">aggressive ×{{ sellable?.tier_coefficients?.aggressive ?? '1.3' }}</el-button>
          <el-button size="small" @click="applyTierCoef('standard')">standard ×{{ sellable?.tier_coefficients?.standard ?? '1.6' }}</el-button>
          <el-button size="small" @click="applyTierCoef('conservative')">conservative ×{{ sellable?.tier_coefficients?.conservative ?? '2.0' }}</el-button>
        </div>
        <el-select
          :model-value="''"
          :placeholder="t('sales.pickModel')"
          size="small"
          class="block"
          filterable
          @change="(v: string) => { const row = sellable?.items.find((r) => r.canonical_name === v); if (row) addQuoteItem(row) }"
        >
          <el-option v-for="r in filteredSellable" :key="`${r.credential_id}-${r.provider_model_id}`" :label="`${r.canonical_name}（${r.supplier_in_per_1m}/${r.supplier_out_per_1m} ${r.supplier_currency}）`" :value="r.canonical_name" />
        </el-select>
        <el-table v-if="quoteItems.length" :data="quoteItems" size="small" class="block">
          <el-table-column prop="canonical_name" :label="t('sales.colModel')" min-width="140" />
          <el-table-column :label="t('sales.colSupplierPrice')" width="130">
            <template #default="{ row }">
              {{ row.cost_in ?? '—' }} / {{ row.cost_out ?? '—' }} {{ row.cost_currency }}
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.tokensIn')" width="130">
            <template #default="{ row }">
              <el-input-number v-model="row.est_tokens_in" size="small" :min="0" :controls="false" />
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.tokensOut')" width="130">
            <template #default="{ row }">
              <el-input-number v-model="row.est_tokens_out" size="small" :min="0" :controls="false" />
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.rateIn')" width="110">
            <template #default="{ row }">
              <el-input-number v-model="row.rate_in" size="small" :min="0" :controls="false" />
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.rateOut')" width="110">
            <template #default="{ row }">
              <el-input-number v-model="row.rate_out" size="small" :min="0" :controls="false" />
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.discount')" width="110">
            <template #default="{ row }">
              <el-input-number v-model="row.discount_pct" size="small" :min="0" :max="100" :controls="false" />
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.colActions')" width="80">
            <template #default="{ $index }">
              <el-button size="small" type="danger" text @click="removeQuoteItem($index)">{{ t('common.delete') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
        <template #footer>
          <el-button @click="quoteDialogVisible = false">{{ t('common.cancel') }}</el-button>
          <el-button type="primary" @click="createNewQuote">{{ t('common.save') }}</el-button>
        </template>
      </el-dialog>

      <!-- 收口弹窗（2026-11-08 前端审计 P3：window.prompt → el-dialog，
           与需求池收口同形态） -->
      <el-dialog v-model="quoteCloseDialogVisible" :title="t('sales.quoteCloseTitle', { id: quoteCloseTarget?.id ?? 0 })" width="420px" class="sales-dialog">
        <el-form label-width="90px">
          <el-form-item :label="t('sales.colStatus')">
            <el-select v-model="quoteCloseForm.status">
              <el-option label="won" value="won" />
              <el-option label="lost" value="lost" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('sales.reason')">
            <el-input v-model="quoteCloseForm.reason" type="textarea" :rows="2" :placeholder="t('sales.reasonRequired')" />
          </el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="quoteCloseDialogVisible = false">{{ t('common.cancel') }}</el-button>
          <el-button type="primary" @click="saveQuoteClose">{{ t('common.save') }}</el-button>
        </template>
      </el-dialog>

      <el-dialog v-model="approvalsDialogVisible" :title="t('sales.approvalsTitle', { id: approvalsQuoteId ?? 0 })" width="640px" class="sales-dialog">
        <el-table :data="approvals" size="small">
          <el-table-column prop="decision" :label="t('sales.colDecision')" width="110" />
          <el-table-column prop="approver" :label="t('sales.colDecidedBy')" width="120" />
          <el-table-column :label="t('sales.triggerReasons')" min-width="240">
            <template #default="{ row }">
              {{ formatTriggerReasons(String(row.trigger_reasons || '')) || '—' }}
            </template>
          </el-table-column>
          <el-table-column prop="comment" :label="t('sales.reason')" min-width="120" />
        </el-table>
      </el-dialog>

      <el-dialog v-model="versionsDialogVisible" :title="t('sales.versionsTitle', { id: versionsQuoteId ?? 0 })" width="560px" class="sales-dialog">
        <el-table :data="versions" size="small">
          <el-table-column prop="version_no" :label="t('sales.colVersion')" width="70" />
          <el-table-column prop="price_confidence" :label="t('sales.colConfidence')" width="130" />
          <el-table-column prop="tier" :label="t('sales.tier')" width="110" />
          <el-table-column :label="t('sales.colMargin')" width="100">
            <template #default="{ row }">
              {{ row.margin_pct != null ? row.margin_pct.toFixed(1) + '%' : '—' }}
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.supersededBy')" width="110">
            <template #default="{ row }">{{ row.superseded_by ?? '—' }}</template>
          </el-table-column>
        </el-table>
      </el-dialog>

      <el-dialog v-model="sharesDialogVisible" :title="t('sales.sharesTitle', { id: sharesQuoteId ?? 0 })" width="620px" class="sales-dialog">
        <div v-if="lastShareToken" class="share-token-hint">
          <el-alert :title="t('sales.shareTokenOnce')" type="success" :closable="false" />
          <code class="share-token">{{ lastShareToken }}</code>
        </div>
        <el-form inline>
          <el-form-item :label="t('sales.contactName')">
            <el-input v-model="shareForm.contact_name" style="width: 140px" />
          </el-form-item>
          <el-form-item :label="t('sales.contactEmail')">
            <el-input v-model="shareForm.contact_email" style="width: 180px" />
          </el-form-item>
          <el-form-item>
            <el-button size="small" type="primary" :disabled="sharesQuoteId === null" @click="sharesQuoteId !== null && mintShare(sharesQuoteId)">
              {{ t('sales.mintShare') }}
            </el-button>
          </el-form-item>
        </el-form>
        <el-table :data="shareLinks" size="small">
          <el-table-column prop="contact_name" :label="t('sales.contactName')" min-width="110" />
          <el-table-column prop="view_count" :label="t('sales.viewCount')" width="90" />
          <el-table-column :label="t('sales.colStatus')" width="110">
            <template #default="{ row }">
              <el-tag :type="row.revoked_at ? 'danger' : 'success'" size="small">
                {{ row.revoked_at ? t('sales.revoked') : t('sales.active') }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('sales.colActions')" width="100">
            <template #default="{ row }">
              <el-button v-if="!row.revoked_at" size="small" type="danger" text @click="revokeShare(row as unknown as ShareLink)">
                {{ t('sales.revoke') }}
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-dialog>
    </div>

    <!-- ── 管理层看板（W2-T2，super-only） ──────────────────────────── -->
    <div v-else-if="tab === 'dashboard' && canDecide">
      <div v-loading="loading">
        <template v-if="dashboard">
          <el-alert
            v-if="dashboard.trigger_rate_alert"
            :title="t('sales.dashboardTriggerAlert', { rate: dashboard.trigger_rate_pct.toFixed(0), line: dashboard.trigger_alert_pct })"
            type="warning"
            show-icon
            :closable="false"
            class="block"
          />
          <div class="dash-funnel block">
            <div v-for="k in funnelOrder" :key="k" class="dash-stat">
              <div class="dash-stat-num">{{ dashboard.funnel[k] ?? 0 }}</div>
              <div class="dash-stat-label">{{ t(`sales.funnel_${k}`) }}</div>
            </div>
          </div>
          <div class="dash-grid block">
            <div class="dash-card">
              <h3>{{ t('sales.dashboardTriggerRate') }}</h3>
              <p class="dash-big">{{ dashboard.trigger_rate_pct.toFixed(1) }}%</p>
              <p class="dash-sub">{{ t('sales.dashboardTriggerDetail', { triggered: dashboard.triggered_quotes, submitted: dashboard.submitted_quotes }) }}</p>
              <ul class="dash-list">
                <li v-for="(n, dim) in dashboard.triggers_by_dimension" :key="dim">{{ dim }}: {{ n }}</li>
                <li v-if="!Object.keys(dashboard.triggers_by_dimension).length">—</li>
              </ul>
            </div>
            <div class="dash-card">
              <h3>{{ t('sales.dashboardLostReasons') }}</h3>
              <ul class="dash-list">
                <li v-for="rc in dashboard.lost_reasons" :key="rc.reason">{{ rc.reason }}: {{ rc.count }}</li>
                <li v-if="!dashboard.lost_reasons.length">—</li>
              </ul>
            </div>
            <div class="dash-card">
              <h3>{{ t('sales.dashboardMarginBuckets') }}</h3>
              <ul class="dash-list">
                <li v-for="(n, b) in dashboard.margin_buckets" :key="b">{{ b }}%: {{ n }}</li>
                <li v-if="!Object.keys(dashboard.margin_buckets).length">—</li>
              </ul>
            </div>
          </div>
          <el-alert
            :title="t('sales.dashboardRealizedPending')"
            type="info"
            show-icon
            :closable="false"
            class="block"
          />
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sales-workbench {
  padding: var(--kx-space-4, 16px);
}
.block {
  margin-top: var(--kx-space-3, 12px);
}
.narrow {
  max-width: 320px;
}
.share-token-hint {
  margin-bottom: 8px;
}
.share-token {
  display: block;
  margin-top: var(--kx-space-2, 8px);
  padding: var(--kx-space-2, 8px);
  background: var(--el-fill-color-light, #f5f7fa);
  border-radius: 4px;
  word-break: break-all;
  font-size: 12px;
}
/* ui-audit B 契约：居中弹层必须有 max-height + 滚动（长内容不裁切） */
.sales-dialog {
  max-height: 65vh;
  overflow-y: auto;
}
.drift-tag {
  margin-left: 4px;
}
.tier-quick {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 0;
}
.tier-quick-label {
  color: var(--el-text-color-secondary, #909399);
  font-size: 13px;
}
/* 看板（W2-T2）：四数漏斗 + 三卡。间距走 4/8/12/16 阶。 */
.dash-funnel {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}
.dash-stat {
  min-width: 96px;
  padding: 12px 16px;
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
  border-radius: 8px;
  text-align: center;
}
.dash-stat-num {
  font-size: 24px;
  font-weight: 600;
}
.dash-stat-label {
  color: var(--el-text-color-secondary, #909399);
  font-size: 12px;
  margin-top: 4px;
}
.dash-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 12px;
}
.dash-card {
  border: 1px solid var(--el-border-color-lighter, #ebeef5);
  border-radius: 8px;
  padding: 12px 16px;
}
.dash-card h3 {
  font-size: 14px;
  margin: 0 0 8px;
  font-weight: 600;
}
.dash-big {
  font-size: 22px;
  font-weight: 600;
  margin: 0 0 4px;
}
.dash-sub {
  color: var(--el-text-color-secondary, #909399);
  font-size: 12px;
  margin: 0 0 8px;
}
.dash-list {
  list-style: none;
  margin: 0;
  padding: 0;
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
</style>
