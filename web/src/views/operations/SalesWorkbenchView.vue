<script setup lang="ts">
// SalesWorkbenchView.vue — 报价系统 MVP 工作台（2026-11-05 W1-T2 前端轮）。
// 三页签：供给池（sellable+登记）/ 需求池 / 报价单（版本+审批+分享）。
// 后端：admin/sales_handlers.go + sales 包（942–949）。
//
// 纪律（2026-11-04 实施规划 §6 W1-T2 验收门）：
//  - withheld 组单独呈现（同名多价整组扣下，不进可售清单）；
//  - C3 置信档逐行展示（trusted 是出正式单的准入，前端不阻断但如实标色）；
//  - 分享 token 只在创建响应出现一次，复制后不可再取（明文不落库）。
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
  closeQuote, createQuote, createQuoteShare, decideQuote, listQuoteApprovals, listQuotes,
  listQuoteShares, listQuoteVersions, listSupplyAnnotations, getSalesSellable,
  sendQuote, submitQuote, upsertSupplyAnnotation,
  type DemandCard, type Quote, type QuoteApproval, type QuoteVersion,
  type SellableList, type ShareLink,
} from '../../api/sales'

const { t } = useI18n()

// 写权限应用层闸（2026-11-06 审计 P-5）：decide 与供给登记写后端是
// super-only（admin/sales_handlers.go decide 内判 salesAuthRole、
// POST supply = h.superAdmin）⇒ 前端对非 super 隐藏对应写按钮（读侧
// 与其余 h.admin 动作不收窄），避免「必 403 的按钮」。
const canDecide = isSuperAdmin()

const tab = ref<'supply' | 'demand' | 'quotes'>('supply')
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
const quoteForm = ref({ customer_name: '', title: '', tier: 'standard' })
const quoteItems = ref<Array<{ canonical_name: string; est_tokens_in: number; est_tokens_out: number; cost_in: number | null; cost_out: number | null; rate_in: number | null; rate_out: number | null; discount_pct: number }>>([])
const versions = ref<QuoteVersion[]>([])
const versionsQuoteId = ref<number | null>(null)
const shareLinks = ref<ShareLink[]>([])
const lastShareToken = ref('')
const shareForm = ref({ contact_name: '', contact_email: '' })
const annotateDialogVisible = ref(false)
const versionsDialogVisible = ref(false)
const sharesDialogVisible = ref(false)

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
  tab.value = name as 'supply' | 'demand' | 'quotes'
  await reload()
}

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
function addItem() {
  quoteItems.value.push({ canonical_name: '', est_tokens_in: 0, est_tokens_out: 0, cost_in: null, cost_out: null, rate_in: null, rate_out: null, discount_pct: 0 })
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
        est_tokens_in: it.est_tokens_in,
        est_tokens_out: it.est_tokens_out,
        cost_in_per_1m: it.cost_in,
        cost_out_per_1m: it.cost_out,
        rate_in: it.rate_in,
        rate_out: it.rate_out,
        discount_pct: it.discount_pct,
      })),
    })
    quoteForm.value = { customer_name: '', title: '', tier: 'standard' }
    quoteItems.value = []
    await reload()
  } catch (e) {
    ElMessage.error(String((e as Error).message || e))
  }
}

async function act(q: Quote, action: 'submit' | 'send' | 'won' | 'lost') {
  try {
    if (action === 'submit') {
      await submitQuote(q.id)
    } else if (action === 'send') {
      await sendQuote(q.id)
    } else {
      const reason = window.prompt(t('sales.closeReasonPrompt'), '')
      if (!reason || !reason.trim()) return
      await closeQuote(q.id, action, reason.trim())
    }
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
    versionsQuoteId.value = q.id
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
    <div v-else>
      <el-form inline class="block">
        <el-form-item :label="t('sales.customer')">
          <el-input v-model="quoteForm.customer_name" style="width: 150px" />
        </el-form-item>
        <el-form-item :label="t('sales.quoteTitle')">
          <el-input v-model="quoteForm.title" style="width: 160px" />
        </el-form-item>
        <el-form-item :label="t('sales.tier')">
          <el-select v-model="quoteForm.tier">
            <el-option label="aggressive" value="aggressive" />
            <el-option label="standard" value="standard" />
            <el-option label="conservative" value="conservative" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button @click="addItem">{{ t('sales.addItem') }}</el-button>
          <el-button type="primary" @click="createNewQuote">{{ t('sales.createQuote') }}</el-button>
        </el-form-item>
      </el-form>

      <el-table v-if="quoteItems.length" :data="quoteItems" size="small" class="block">
        <el-table-column :label="t('sales.colModel')" min-width="150">
          <template #default="{ row }">
            <el-input v-model="row.canonical_name" size="small" />
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
        <el-table-column :label="t('sales.costIn')" width="110">
          <template #default="{ row }">
            <el-input-number v-model="row.cost_in" size="small" :min="0" :controls="false" />
          </template>
        </el-table-column>
        <el-table-column :label="t('sales.costOut')" width="110">
          <template #default="{ row }">
            <el-input-number v-model="row.cost_out" size="small" :min="0" :controls="false" />
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
      </el-table>

      <el-table v-loading="loading" :data="quotes" size="small" class="block">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column prop="customer_name" :label="t('sales.customer')" min-width="130" />
        <el-table-column prop="title" :label="t('sales.quoteTitle')" min-width="130" />
        <el-table-column :label="t('sales.colStatus')" width="140">
          <template #default="{ row }">
            <el-tag :type="row.version?.expired ? 'danger' : row.status === 'won' ? 'success' : row.status === 'pending_approval' ? 'warning' : 'info'" size="small">
              {{ row.version?.expired ? t('sales.expired') : row.status }}
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

      <el-dialog v-model="sharesDialogVisible" :title="t('sales.sharesTitle', { id: versionsQuoteId ?? 0 })" width="560px" class="sales-dialog">
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
            <el-button size="small" type="primary" :disabled="versionsQuoteId === null" @click="versionsQuoteId !== null && mintShare(versionsQuoteId)">
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
        </el-table>
      </el-dialog>
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
</style>
