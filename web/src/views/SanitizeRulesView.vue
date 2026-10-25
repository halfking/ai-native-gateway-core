<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { isSuperAdmin, isTenantAdmin } from "../store";
import { getSetting, updateSetting } from "../api/settings";
import {
  RULE_TYPES,
  RULE_KINDS,
  RULE_EXEMPTIONS,
  getSanitizeRules,
  validateSanitizeRules,
  publishSanitizeRules,
  listSanitizeRuleEvents,
  listSanitizeRuleVersions,
  listSanitizeRuleInstances,
  reviewSanitizeRuleEvent,
  rollbackSanitizeRules,
  isRuleConfig,
  type RuleConfig,
  type SensitiveRule,
  type RulePhase,
  type RuleCurrent,
  type RuleEvent,
  type RuleVersion,
  type RuleInstance,
  type ReviewStatus,
} from "../api/sanitize-rules";
import PageHeader from "../components/ui/PageHeader.vue";
import { fmtDateTime24h } from "../i18n/useFormat";
const { t } = useI18n();
const labels = computed<Record<string, string>>(() => ({
  rules: t("sanitizeRules.rules"),
  events: t("sanitizeRules.events"),
  versions: t("sanitizeRules.versions"),
  instances: t("sanitizeRules.instances"),
  parameters: t("sanitizeRules.parameters"),
  pending: t("sanitizeRules.pending"),
  confirmed_sensitive: t("sanitizeRules.confirmed_sensitive"),
  false_positive: t("sanitizeRules.false_positive"),
  ignored: t("sanitizeRules.ignored"),
  added: t("sanitizeRules.added"),
  removed: t("sanitizeRules.removed"),
  changed: t("sanitizeRules.changed"),
}));
const superAdmin = computed(() => isSuperAdmin());
const allowed = computed(() => superAdmin.value || isTenantAdmin());
const tab = ref(superAdmin.value ? "rules" : "events");
const head = ref<RuleCurrent | null>(null),
  draft = ref<RuleConfig>({ rules: [] }),
  selected = ref(-1),
  loading = ref(false),
  busy = ref(false);
const error = ref(""),
  notice = ref(""),
  validated = ref(""),
  comment = ref(""),
  conflict = ref(false),
  advanced = ref(""),
  advancedDirty = ref(false),
  validatedDigest = ref("");
const events = ref<RuleEvent[]>([]),
  versions = ref<RuleVersion[]>([]),
  instances = ref<RuleInstance[]>([]),
  targetVersion = ref(0);
const eventError = ref(""),
  versionError = ref(""),
  instanceError = ref(""),
  parameterError = ref("");
const eventLoading = ref(false),
  versionLoading = ref(false),
  instanceLoading = ref(false);
const statusFilter = ref(""),
  actionFilter = ref(""),
  reviewComments = ref<Record<number, string>>({}),
  reviewBusy = ref<number | null>(null),
  linked = ref<number[]>([]);
const editingMatched = ref(false);
const expandedEvent = ref<number | null>(null);
const chosenVersion = ref<RuleVersion | null>(null),
  rollbackOpen = ref(false),
  rollbackComment = ref("");
const params = ref({ refresh: 5, audit: true, limit: 10000 }),
  paramsBusy = ref(false),
  paramsLoaded = ref(false),
  paramsLoading = ref(false);
const keys = [
  "sanitize.rules.refresh_interval_seconds",
  "sanitize.rules.audit_enabled",
  "sanitize.rules.event_limit",
];
const activeRule = computed(() => draft.value.rules[selected.value]);
const dirty = computed(
  () =>
    !!head.value &&
    JSON.stringify(draft.value) !== JSON.stringify(head.value.config),
);
const serialized = computed(() => JSON.stringify(draft.value));
const valid = computed(
  () => validated.value !== "" && validated.value === serialized.value,
);
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const message = (e: unknown) =>
  e instanceof Error ? e.message : t("sanitizeRules.unknownError");
let disposed = false,
  pollTimer: ReturnType<typeof setTimeout> | undefined;
const controller = new AbortController();
let instanceController: AbortController | null = null;
watch(
  draft,
  () => {
    validated.value = "";
    if (notice.value === t("sanitizeRules.valid")) notice.value = "";
  },
  { deep: true },
);
function applyHead(value: RuleCurrent) {
  const config = clone(value.config);
  for (const rule of config.rules) {
    rule.samples ??= [];
    rule.exemptions ??= [];
  }
  head.value = { ...clone(value), config };
  draft.value = clone(config);
  selected.value = -1;
  validated.value = "";
  linked.value = [];
  editingMatched.value = false;
  comment.value = "";
  conflict.value = false;
  advanced.value = "";
  advancedDirty.value = false;
}
async function loadCurrent(force = false) {
  if (!superAdmin.value) return;
  if (
    (dirty.value || advancedDirty.value) &&
    !force &&
    !window.confirm(t("sanitizeRules.discard"))
  )
    return;
  loading.value = true;
  error.value = "";
  try {
    const value = await getSanitizeRules({ signal: controller.signal });
    if (!disposed) applyHead(value);
  } catch (e) {
    if (!disposed) error.value = message(e);
  } finally {
    if (!disposed) loading.value = false;
  }
}
async function loadEvents() {
  if (!allowed.value || eventLoading.value) return;
  eventLoading.value = true;
  eventError.value = "";
  try {
    const value = await listSanitizeRuleEvents(
      { action: actionFilter.value, status: statusFilter.value },
      { signal: controller.signal },
    );
    if (!disposed) events.value = value.events ?? [];
  } catch (e) {
    if (!disposed) eventError.value = message(e);
  } finally {
    if (!disposed) eventLoading.value = false;
  }
}
async function loadVersions() {
  if (!superAdmin.value || versionLoading.value) return;
  versionLoading.value = true;
  versionError.value = "";
  try {
    const value = await listSanitizeRuleVersions({ signal: controller.signal });
    if (!disposed) versions.value = value.versions ?? [];
  } catch (e) {
    if (!disposed) versionError.value = message(e);
  } finally {
    if (!disposed) versionLoading.value = false;
  }
}
async function loadInstances() {
  if (!superAdmin.value || instanceLoading.value || document.hidden || disposed)
    return;
  const request = new AbortController();
  instanceController = request;
  instanceLoading.value = true;
  try {
    const value = await listSanitizeRuleInstances({ signal: request.signal });
    if (!disposed && !request.signal.aborted) {
      instances.value = value.instances ?? [];
      targetVersion.value = value.target_version;
      instanceError.value = "";
    }
  } catch (e) {
    if (!disposed && !request.signal.aborted) instanceError.value = message(e);
  } finally {
    if (instanceController === request) {
      instanceController = null;
      if (!disposed) instanceLoading.value = false;
    }
  }
}
async function loadParams() {
  if (!superAdmin.value || paramsLoading.value || paramsBusy.value) return;
  paramsLoading.value = true;
  paramsLoaded.value = false;
  parameterError.value = "";
  try {
    const values = await Promise.all(keys.map((key) => getSetting(key)));
    if (!disposed) {
      params.value = {
        refresh: Number(values[0].value),
        audit: Boolean(values[1].value),
        limit: Number(values[2].value),
      };
      paramsLoaded.value = true;
    }
  } catch (e) {
    if (!disposed) parameterError.value = message(e);
  } finally {
    if (!disposed) paramsLoading.value = false;
  }
}
function schedulePoll() {
  clearTimeout(pollTimer);
  if (disposed || !superAdmin.value || document.hidden) return;
  pollTimer = setTimeout(async () => {
    await loadInstances();
    schedulePoll();
  }, 5000);
}
function visibility() {
  if (document.hidden) {
    clearTimeout(pollTimer);
    instanceController?.abort();
    instanceController = null;
    instanceLoading.value = false;
    return;
  }
  void loadInstances();
  schedulePoll();
}
function chooseTab(value: string) {
  tab.value = value;
  if (value === "events") void loadEvents();
  if (value === "versions") void loadVersions();
  if (value === "instances") void loadInstances();
}
function addRule() {
  const id = "custom." + Date.now().toString(36);
  draft.value.rules.push({
    id,
    name: t("sanitizeRules.add"),
    type: "custom",
    kind: "pattern",
    pattern: "example-secret",
    group: 0,
    scopes: ["input"],
    action: "mask",
    enabled: true,
    priority: 0,
    samples: [],
    exemptions: [],
  });
  selected.value = draft.value.rules.length - 1;
}
function removeRule() {
  draft.value.rules.splice(selected.value, 1);
  selected.value = -1;
}
function setList(key: "tools" | "fields", event: Event) {
  if (activeRule.value)
    activeRule.value[key] = (event.target as HTMLInputElement).value
      .split(",")
      .map((x) => x.trim())
      .filter(Boolean);
}

// ── 按模式启停（2026-10-20）────────────────────────────────────────────────
// 运行期判定见 domains/sanitizerules/model.go 的 Applies()：
//     r.Enabled && 命中 scope && 命中 tenant
// 所以「某规则在某模式上是否生效」= 全局开关 ∧ scopes 含该模式。
// 这里的开关直接改这两个字段，**不改数据模型**（不需要迁移），
// 保存路径与既有的 validate → publish 完全一致。
const RULE_PHASES = ["input", "output", "egress"] as const satisfies readonly RulePhase[];

function phaseActive(rule: SensitiveRule, phase: RulePhase): boolean {
  return !!rule.enabled && (rule.scopes || []).includes(phase);
}

function onPhaseToggle(rule: SensitiveRule, phase: RulePhase, event: Event) {
  const on = (event.target as HTMLInputElement).checked;
  const scopes = new Set(rule.scopes || []);
  if (on) {
    scopes.add(phase);
    // 勾上某模式 ⇒ 这条规则在该模式上要真的开着，所以把全局开关也带上。
    rule.enabled = true;
  } else {
    scopes.delete(phase);
  }
  // ⚠ 保留原有顺序：comparableRule() 用 JSON.stringify 比 scopes，
  //   顺序一改就会让「版本差异」把纯顺序变化报成一条 changed。
  const ordered = (rule.scopes || []).filter((p) => scopes.has(p));
  for (const p of RULE_PHASES) {
    if (scopes.has(p) && !ordered.includes(p)) ordered.push(p);
  }
  rule.scopes = ordered;
}
function openAdvanced() {
  if (!advancedDirty.value)
    advanced.value = JSON.stringify(draft.value, null, 2);
}
function applyAdvanced() {
  try {
    const parsed: unknown = JSON.parse(advanced.value);
    if (!isRuleConfig(parsed)) throw new Error();
    draft.value = parsed;
    for (const rule of draft.value.rules) {
      rule.samples ??= [];
      rule.exemptions ??= [];
    }
    selected.value = -1;
    advancedDirty.value = false;
    advanced.value = JSON.stringify(draft.value, null, 2);
    error.value = "";
  } catch {
    error.value = t("sanitizeRules.invalidJson");
  }
}
async function validate() {
  if (busy.value) return;
  if (advancedDirty.value) {
    applyAdvanced();
    if (advancedDirty.value) return;
  }
  busy.value = true;
  error.value = "";
  try {
    const requestConfig = clone(draft.value);
    const requestSerialized = JSON.stringify(requestConfig);
    const result = await validateSanitizeRules(requestConfig);
    if (result.valid) {
      validated.value = requestSerialized;
      validatedDigest.value = result.config_digest;
      notice.value = valid.value ? t("sanitizeRules.valid") : "";
    }
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
async function publish() {
  if (busy.value || !head.value) return;
  if (!comment.value.trim()) {
    error.value = t("sanitizeRules.commentRequired");
    return;
  }
  if (!valid.value) {
    error.value = t("sanitizeRules.validationRequired");
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    const result = await publishSanitizeRules({
      base_version: head.value.version,
      config: clone(draft.value),
      comment: comment.value.trim(),
      ...(linked.value.length ? { review_event_ids: [...linked.value] } : {}),
    });
    applyHead({
      version: result.id,
      config: result.config,
      config_digest: validatedDigest.value,
      status: head.value.status,
    });
    notice.value = t("sanitizeRules.published");
    void loadInstances();
    void loadVersions();
    void loadEvents();
  } catch (e) {
    conflict.value =
      !!e && typeof e === "object" && "status" in e && e.status === 409;
    error.value = conflict.value ? t("sanitizeRules.conflict") : message(e);
  } finally {
    busy.value = false;
  }
}
async function review(
  event: RuleEvent,
  status: Exclude<ReviewStatus, "pending">,
) {
  if (reviewBusy.value !== null) return;
  reviewBusy.value = event.id;
  eventError.value = "";
  try {
    await reviewSanitizeRuleEvent(event.id, {
      status,
      comment: reviewComments.value[event.id] ?? "",
    });
    event.status = status;
    notice.value = t("sanitizeRules.reviewed");
  } catch (e) {
    eventError.value = message(e);
  } finally {
    reviewBusy.value = null;
  }
}
function draftEvent(event: RuleEvent, original = false) {
  const rule = head.value?.config.rules.find((r) => r.id === event.rule_id);
  if (!rule) {
    eventError.value = t("sanitizeRules.missingRule");
    return;
  }
  const targetID = original ? rule.id : rule.id.slice(0, 100) + ".review." + event.id;
  let index = draft.value.rules.findIndex((r) => r.id === targetID);
  if (index < 0 && !original) {
    const copy = clone(rule);
    copy.id = targetID;
    copy.name = rule.name + " · " + event.id;
    copy.tenant_id = event.tenant_id;
    copy.scopes = [event.phase];
    if (event.phase !== "egress") copy.exemptions = [];
    draft.value.rules.push(copy);
    index = draft.value.rules.length - 1;
  }
  if (index < 0) {
    eventError.value = t("sanitizeRules.missingRule");
    return;
  }
  selected.value = index;
  editingMatched.value = original;
  linked.value = [...new Set([...linked.value, event.id])];
  tab.value = "rules";
  notice.value = t(original ? "sanitizeRules.editMatchedHint" : "sanitizeRules.scopedHint");
}
function comparableRule(rule: SensitiveRule) {
  // The editor fills omitted lists for v-model. Compare policy values rather
  // than JSON property order or optional empty-list serialization.
  return JSON.stringify([
    rule.id, rule.name, rule.type, rule.kind, rule.pattern, rule.group,
    rule.scopes, rule.action, rule.enabled, rule.priority,
    rule.tenant_id ?? "", rule.tools ?? [], rule.fields ?? [],
    rule.samples ?? [], rule.exemptions ?? [],
  ]);
}
const versionDiff = computed(() => {
  if (!chosenVersion.value || !head.value) return [];
  const before = new Map(head.value.config.rules.map((r) => [r.id, r])),
    after = new Map(chosenVersion.value.config.rules.map((r) => [r.id, r]));
  const rows: { id: string; change: string }[] = [];
  for (const [id, rule] of after) {
    if (!before.has(id)) rows.push({ id, change: "added" });
    else if (comparableRule(before.get(id)!) !== comparableRule(rule))
      rows.push({ id, change: "changed" });
  }
  for (const id of before.keys())
    if (!after.has(id)) rows.push({ id, change: "removed" });
  return rows;
});
async function rollback() {
  if (busy.value || !head.value || !chosenVersion.value) return;
  if (!rollbackComment.value.trim()) {
    versionError.value = t("sanitizeRules.commentRequired");
    return;
  }
  busy.value = true;
  versionError.value = "";
  try {
    const result = await rollbackSanitizeRules({
      base_version: head.value.version,
      target_version: chosenVersion.value.id,
      comment: rollbackComment.value.trim(),
    });
    applyHead({
      version: result.id,
      config: result.config,
      config_digest: "",
      status: head.value.status,
    });
    rollbackOpen.value = false;
    rollbackComment.value = "";
    notice.value = t("sanitizeRules.published");
    void loadVersions();
    void loadInstances();
  } catch (e) {
    versionError.value = message(e);
  } finally {
    busy.value = false;
  }
}
async function saveParams() {
  if (paramsBusy.value || paramsLoading.value || !paramsLoaded.value) return;
  paramsBusy.value = true;
  parameterError.value = "";
  const values = [params.value.refresh, params.value.audit, params.value.limit];
  const result = await Promise.allSettled(
    keys.map((key, index) => updateSetting(key, { value: values[index] })),
  );
  if (result.some((x) => x.status === "rejected"))
    parameterError.value = t("sanitizeRules.parametersPartial");
  else notice.value = t("sanitizeRules.parametersSaved");
  paramsBusy.value = false;
}
const date = (value: string) =>
  !value || value.startsWith("0001-")
    ? t("sanitizeRules.never")
    : fmtDateTime24h(value);
const stale = (instance: RuleInstance) =>
  !instance.last_seen ||
  Date.now() - Date.parse(instance.last_seen) >
    Math.max(30000, params.value.refresh * 3000);
onMounted(() => {
  if (superAdmin.value) {
    void loadCurrent(true);
    void loadVersions();
    void loadInstances();
    void loadParams();
    schedulePoll();
  }
  void loadEvents();
  document.addEventListener("visibilitychange", visibility);
});
onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(pollTimer);
  controller.abort();
  instanceController?.abort();
  document.removeEventListener("visibilitychange", visibility);
});
</script>
<template>
  <main class="sanitize-rules-view">
    <PageHeader
      :title="t('sanitizeRules.title')"
      :subtitle="t('sanitizeRules.subtitle')"
      ><template #actions
        ><RouterLink to="/admin/modules" class="btn">{{
          t("sanitizeRules.settingsLink")
        }}</RouterLink
        ><button
          v-if="superAdmin"
          class="btn"
          :disabled="loading || busy"
          @click="loadCurrent()"
        >
          {{ t("sanitizeRules.refresh") }}
        </button></template
      ></PageHeader
    >
    <p v-if="!allowed" role="alert" class="error-banner">
      {{ t("sanitizeRules.permission") }}
    </p>
    <template v-else>
      <div v-if="head" class="provenance">
        <span
          >{{ t("sanitizeRules.base") }}
          <strong>{{ head.version }}</strong></span
        ><span
          >{{ t("sanitizeRules.target") }}
          <strong>{{ targetVersion }}</strong></span
        ><span v-if="head.version === 0" class="warning">{{
          t("sanitizeRules.fallback")
        }}</span
        ><small :title="head.config_digest"
          >{{ t("sanitizeRules.digest") }}
          <code>{{ head.config_digest.slice(0, 16) || "—" }}</code></small
        >
      </div>
      <p v-if="notice" role="status" class="notice">{{ notice }}</p>
      <p v-if="error" role="alert" class="error-banner">{{ error }}</p>
      <p v-if="instanceError" role="alert" class="error-banner">
        {{ t("sanitizeRules.instances") }}: {{ instanceError }}
      </p>
      <nav class="tabs" :aria-label="t('sanitizeRules.title')">
        <button
          v-for="key in superAdmin
            ? ['rules', 'events', 'versions', 'instances', 'parameters']
            : ['events']"
          :key="key"
          class="tab"
          :class="{ active: tab === key }"
          :data-test="'tab-' + key"
          :aria-current="tab === key ? 'page' : undefined"
          @click="chooseTab(key)"
        >
          {{ labels[key] }}
        </button>
      </nav>
      <section v-if="tab === 'rules' && superAdmin" :aria-busy="loading">
        <p v-if="loading">{{ t("sanitizeRules.loading") }}</p>
        <template v-else-if="head">
          <div class="toolbar">
            <strong
              >{{ t("sanitizeRules.draft") }} · {{ draft.rules.length }}</strong
            ><button
              class="btn"
              data-test="add-rule"
              :disabled="busy"
              @click="addRule"
            >
              {{ t("sanitizeRules.add") }}</button
            ><button
              v-if="dirty || advancedDirty"
              class="btn"
              :disabled="busy"
              @click="loadCurrent()"
            >
              {{ t("sanitizeRules.reset") }}
            </button>
          </div>
          <div class="rule-workspace">
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{{ t("sanitizeRules.rule") }}</th>
                    <th :title="t('sanitizeRules.perModeHint')">
                      {{ t("sanitizeRules.perMode") }}
                    </th>
                    <th>{{ t("sanitizeRules.action") }}</th>
                    <th>{{ t("sanitizeRules.enabled") }}</th>
                    <th>{{ t("sanitizeRules.edit") }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="(rule, index) in draft.rules"
                    :key="rule.id + '-' + index"
                    data-test="rule-row"
                    :class="{ selected: index === selected }"
                  >
                    <td>
                      <strong>{{ rule.name }}</strong
                      ><small
                        ><code>{{ rule.id }}</code> · {{ rule.type }}</small
                      ><small>{{
                        rule.tenant_id || t("sanitizeRules.all")
                      }}</small>
                    </td>
                    <td>
                      <!--
                        ★ 按模式启停（2026-10-20 审计轮补齐）。
                        运行期的判定是 domains/sanitizerules/model.go 的
                        Applies(phase, tenant, tool, field)：
                            r.Enabled && scope 命中 && tenant 命中
                        也就是说「这条规则在某个模式上是否生效」= **全局开关 ∧ 该模式在 scopes 里**。
                        此前 UI 只有一列 scopes 文本（只读）与一个全局 enabled 开关，
                        「按模式停用某条规则」只能去编辑器里手工摘 scope —— 表达力有，
                        但看不出来「现在这条规则在哪些模式上是真的开着的」。
                        这里把两个维度合成一排可点的开关：勾上 = 该模式生效。
                      -->
                      <fieldset
                        class="mode-matrix"
                        data-test="rule-mode-matrix"
                        :aria-label="
                          t('sanitizeRules.perMode') + ' ' + rule.name
                        "
                      >
                        <label
                          v-for="phase in RULE_PHASES"
                          :key="phase"
                          class="check"
                        ><input
                          type="checkbox"
                          :checked="phaseActive(rule, phase)"
                          :disabled="busy"
                          :data-test="'rule-mode-' + phase"
                          :aria-label="
                            phase + ' ' + t('sanitizeRules.perMode') + ' ' + rule.name
                          "
                          @change="onPhaseToggle(rule, phase, $event)"
                        />{{ phase }}</label
                        >
                      </fieldset>
                    </td>
                    <td>
                      <code>{{ rule.action }}</code>
                    </td>
                    <td>
                      <input
                        v-model="rule.enabled"
                        type="checkbox"
                        :aria-label="
                          t('sanitizeRules.enabled') + ' ' + rule.name
                        "
                        :disabled="busy"
                      />
                    </td>
                    <td>
                      <button
                        class="btn"
                        data-test="edit-rule"
                        :disabled="busy"
                        @click="selected = index"
                      >
                        {{ t("sanitizeRules.edit") }}
                      </button>
                    </td>
                  </tr>
                  <tr v-if="!draft.rules.length">
                    <td colspan="5">{{ t("sanitizeRules.noRules") }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <form
              v-if="activeRule"
              class="editor card"
              @submit.prevent="validate"
            >
              <h3>{{ t("sanitizeRules.edit") }}</h3>
              <label
                >{{ t("sanitizeRules.ruleId")
                }}<input v-model="activeRule.id" :disabled="busy" /></label
              ><label
                >{{ t("sanitizeRules.name")
                }}<input
                  v-model="activeRule.name"
                  data-test="rule-name"
                  :disabled="busy"
              /></label>
              <div class="form-grid">
                <label
                  >{{ t("sanitizeRules.type")
                  }}<select v-model="activeRule.type" :disabled="busy">
                    <option v-for="type in RULE_TYPES" :key="type">
                      {{ type }}
                    </option>
                  </select></label
                ><label
                  >{{ t("sanitizeRules.kind")
                  }}<select v-model="activeRule.kind" :disabled="busy">
                    <option v-for="kind in RULE_KINDS" :key="kind">
                      {{ kind }}
                    </option>
                  </select></label
                >
              </div>
              <p v-if="activeRule.kind === 'review_combination'" class="muted">
                {{ t("sanitizeRules.combinationHint") }}
              </p>
              <label
                >{{ t("sanitizeRules.pattern")
                }}<textarea
                  v-model="activeRule.pattern"
                  rows="3"
                  :disabled="busy"
                  spellcheck="false"
                />
              </label>
              <div class="form-grid">
                <label
                  >{{ t("sanitizeRules.group")
                  }}<input
                    v-model.number="activeRule.group"
                    type="number"
                    min="0"
                    :disabled="busy" /></label
                ><label
                  >{{ t("sanitizeRules.priority")
                  }}<input
                    v-model.number="activeRule.priority"
                    type="number"
                    min="-10000"
                    max="10000"
                    :disabled="busy" /></label
                ><label
                  >{{ t("sanitizeRules.action")
                  }}<select v-model="activeRule.action" :disabled="busy">
                    <option>mask</option>
                    <option>block</option>
                    <option>log</option>
                  </select></label
                ><label
                  >{{ t("sanitizeRules.tenant")
                  }}<input
                    v-model="activeRule.tenant_id"
                    data-test="rule-tenant"
                    :disabled="busy"
                /></label>
              </div>
              <fieldset>
                <legend>{{ t("sanitizeRules.scopes") }}</legend>
                <label
                  v-for="phase in ['input', 'output', 'egress'] as const"
                  :key="phase"
                  class="check"
                  ><input
                    v-model="activeRule.scopes"
                    type="checkbox"
                    :value="phase"
                    :disabled="busy"
                  />{{ phase }}</label
                >
              </fieldset>
              <label
                >{{ t("sanitizeRules.tools")
                }}<input
                  :value="activeRule.tools?.join(', ')"
                  :disabled="busy"
                  @input="setList('tools', $event)" /></label
              ><label
                >{{ t("sanitizeRules.fields")
                }}<input
                  :value="activeRule.fields?.join(', ')"
                  :disabled="busy"
                  @input="setList('fields', $event)"
              /></label>
              <fieldset>
                <legend>{{ t("sanitizeRules.exemptions") }}</legend>
                <p class="muted">{{ t("sanitizeRules.exemptionsHint") }}</p>
                <label v-for="item in RULE_EXEMPTIONS" :key="item" class="check"
                  ><input
                    v-model="activeRule.exemptions"
                    type="checkbox"
                    :value="item"
                    :disabled="busy || !activeRule.scopes.includes('egress')"
                  />{{ item }}</label
                >
              </fieldset>
              <h4>{{ t("sanitizeRules.samples") }}</h4>
              <p class="muted">{{ t("sanitizeRules.safeSamples") }}</p>
              <div
                v-for="(sample, index) in activeRule.samples"
                :key="index"
                class="sample"
              >
                <label
                  >{{ t("sanitizeRules.sampleText")
                  }}<textarea
                    v-model="sample.text"
                    rows="2"
                    :disabled="busy"
                  /></label
                ><label class="check"
                  ><input
                    v-model="sample.match"
                    type="checkbox"
                    :disabled="busy"
                  />{{ t("sanitizeRules.expectedMatch") }}</label
                ><button
                  type="button"
                  class="btn"
                  :disabled="busy"
                  @click="activeRule.samples?.splice(index, 1)"
                >
                  {{ t("sanitizeRules.sampleRemove") }}
                </button>
              </div>
              <button
                type="button"
                class="btn"
                :disabled="busy"
                @click="
                  (activeRule.samples ??= []).push({ text: '', match: true })
                "
              >
                {{ t("sanitizeRules.addSample") }}</button
              ><button
                type="button"
                class="btn danger"
                data-test="delete-rule"
                :disabled="busy"
                @click="removeRule"
              >
                {{ t("sanitizeRules.remove") }}
              </button>
            </form>
          </div>
          <details
            @toggle="
              ($event.target as HTMLDetailsElement).open && openAdvanced()
            "
          >
            <summary>{{ t("sanitizeRules.advanced") }}</summary>
            <textarea
              v-model="advanced"
              class="json-editor"
              @input="advancedDirty = true"
              rows="18"
              spellcheck="false"
              :aria-label="t('sanitizeRules.advanced')"
              :disabled="busy"
            /><button class="btn" :disabled="busy" @click="applyAdvanced">
              {{ t("sanitizeRules.applyJson") }}
            </button>
          </details>
          <div class="publish card">
            <span v-if="linked.length"
              >{{ t("sanitizeRules.linked") }}: {{ linked.join(", ") }}</span
            >
            <p v-if="linked.length" class="muted">
              {{ t(editingMatched ? "sanitizeRules.editMatchedHint" : "sanitizeRules.scopedHint") }}
            </p>
            <label
              >{{ t("sanitizeRules.comment")
              }}<input
                v-model="comment"
                data-test="publish-comment"
                maxlength="2000"
                :disabled="busy"
            /></label>
            <div class="toolbar">
              <button
                class="btn"
                data-test="validate"
                :disabled="busy || !draft.rules.length"
                @click="validate"
              >
                {{ t("sanitizeRules.validate") }}</button
              ><span v-if="valid">{{ t("sanitizeRules.valid") }}</span
              ><button
                class="btn btn-primary"
                data-test="publish"
                :disabled="
                  busy ||
                  !valid ||
                  head.version === 0 ||
                  conflict ||
                  advancedDirty
                "
                @click="publish"
              >
                {{
                  busy ? t("sanitizeRules.busy") : t("sanitizeRules.publish")
                }}
              </button>
            </div>
          </div> </template
        ><button v-else class="btn" @click="loadCurrent(true)">
          {{ t("sanitizeRules.retry") }}
        </button>
      </section>
      <section v-if="tab === 'events'" :aria-busy="eventLoading">
        <p class="muted">{{ t("sanitizeRules.reviewOnly") }}</p>
        <form class="toolbar" @submit.prevent="loadEvents">
          <label
            >{{ t("sanitizeRules.status")
            }}<select v-model="statusFilter">
              <option value="">{{ t("sanitizeRules.all") }}</option>
              <option
                v-for="status in [
                  'pending',
                  'confirmed_sensitive',
                  'false_positive',
                  'ignored',
                ]"
                :key="status"
                :value="status"
              >
                {{ labels[status] }}
              </option>
            </select></label
          ><label
            >{{ t("sanitizeRules.action")
            }}<select v-model="actionFilter">
              <option value="">{{ t("sanitizeRules.all") }}</option>
              <option
                v-for="action in [
                  'mask',
                  'block',
                  'log',
                  'exempted',
                  'pending',
                ]"
                :key="action"
              >
                {{ action }}
              </option>
            </select></label
          ><button class="btn" :disabled="eventLoading">
            {{ t("sanitizeRules.filter") }}
          </button>
        </form>
        <p v-if="eventError" role="alert" class="error-banner">
          {{ eventError }}
        </p>
        <p v-if="eventLoading">{{ t("sanitizeRules.loading") }}</p>
        <p v-else-if="!events.length && !eventError">
          {{ t("sanitizeRules.empty") }}
        </p>
        <p class="event-note">{{ t("sanitizeRules.eventEvidenceNote") }}</p>
        <article v-for="event in events" :key="event.id" class="card event">
          <div class="toolbar">
            <strong>#{{ event.id }} · {{ event.rule_id }}</strong
            ><span
              >{{ event.tenant_id }} · v{{ event.version }} ·
              {{ event.phase }} / {{ event.action }}</span
            ><span>{{ labels[event.status] }}</span
            ><span>×{{ event.count }}</span>
          </div>
          <p class="event-meta">
            <span
              >{{ t("sanitizeRules.type") }}: {{ event.type || "—" }}</span
            >
            ·
            <span
              >{{ t("sanitizeRules.kind") }}: {{ event.field || "—" }}</span
            >
          </p>
          <code>{{ event.sample_masked }}</code
          ><small
            >{{ date(event.first_seen) }} → {{ date(event.last_seen) }}</small
          >
          <div
            v-if="event.matched_rule"
            class="matched-rule"
            :data-test="'matched-rule-' + event.id"
          >
            <button
              class="btn"
              type="button"
              :data-test="'toggle-matched-' + event.id"
              @click="
                expandedEvent = expandedEvent === event.id ? null : event.id
              "
            >
              {{
                expandedEvent === event.id
                  ? t("sanitizeRules.hideMatched")
                  : t("sanitizeRules.viewMatched")
              }}
            </button>
            <div
              v-if="expandedEvent === event.id"
              class="matched-rule__body"
              :data-test="'matched-body-' + event.id"
            >
              <p v-if="event.matched_rule.missing" class="error-banner">
                {{ t("sanitizeRules.missingRule") }}
              </p>
              <template v-else>
                <p>
                  <strong>{{ event.matched_rule.name || event.rule_id }}</strong>
                  · {{ event.matched_rule.type }} /
                  {{ event.matched_rule.kind }} ·
                  {{ (event.matched_rule.scopes || []).join(", ") }} /
                  {{ event.matched_rule.action }}
                  <span v-if="event.matched_rule.enabled === false">
                    · {{ t("sanitizeRules.enabled") }}: off</span
                  >
                </p>
                <label
                  >{{ t("sanitizeRules.pattern")
                  }}<code class="matched-pattern">{{
                    event.matched_rule.pattern
                  }}</code></label
                >
                <ul
                  v-if="event.matched_rule.samples?.length"
                  class="matched-samples"
                >
                  <li
                    v-for="(sample, i) in event.matched_rule.samples"
                    :key="i"
                  >
                    <code>{{ sample.text }}</code>
                    ·
                    {{
                      sample.match
                        ? t("sanitizeRules.expectedMatch")
                        : t("sanitizeRules.expectedMiss")
                    }}
                  </li>
                </ul>
              </template>
            </div>
          </div>
          <p v-if="event.resolved_version">
            {{ t("sanitizeRules.linked") }}: v{{ event.resolved_version }}
          </p>
          <label
            >{{ t("sanitizeRules.reviewComment")
            }}<input
              v-model="reviewComments[event.id]"
              :data-test="'review-comment-' + event.id"
              maxlength="2000"
              :disabled="reviewBusy !== null"
          /></label>
          <div class="toolbar">
            <button
              v-for="status in [
                'confirmed_sensitive',
                'false_positive',
                'ignored',
              ] as const"
              :key="status"
              class="btn"
              :data-test="'review-' + status + '-' + event.id"
              :disabled="reviewBusy !== null"
              @click="review(event, status)"
            >
              {{ labels[status] }}</button
            ><button
              v-if="superAdmin"
              class="btn"
              :data-test="'edit-event-' + event.id"
              :disabled="busy || !head"
              @click="draftEvent(event, true)"
            >
              {{ t("sanitizeRules.editMatched") }}
            </button><button
              v-if="superAdmin"
              class="btn"
              :data-test="'draft-event-' + event.id"
              :disabled="busy || !head"
              @click="draftEvent(event)"
            >
              {{ t("sanitizeRules.draftEvent") }}
            </button>
          </div>
          <small v-if="event.reviewer"
            >{{ event.reviewer }} · {{ event.review_comment }}</small
          >
        </article>
      </section>
      <section v-if="tab === 'versions'" :aria-busy="versionLoading">
        <button class="btn" :disabled="versionLoading" @click="loadVersions">
          {{ t("sanitizeRules.refresh") }}
        </button>
        <p v-if="versionError" role="alert" class="error-banner">
          {{ versionError }}
        </p>
        <p v-if="versionLoading">{{ t("sanitizeRules.loading") }}</p>
        <p v-else-if="!versions.length && !versionError">
          {{ t("sanitizeRules.empty") }}
        </p>
        <div class="history-layout">
          <div>
            <button
              v-for="version in versions"
              :key="version.id"
              class="version-row"
              @click="
                chosenVersion = version;
                rollbackOpen = false;
              "
            >
              <strong>v{{ version.id }}</strong> · {{ version.actor }} ·
              {{ date(version.created_at) }}<small>{{ version.comment }}</small
              ><small
                >{{ t("sanitizeRules.parent") }}: {{ version.parent_id }}
                <span v-if="version.rollback_of"
                  >· {{ t("sanitizeRules.rollbackOf") }}:
                  {{ version.rollback_of }}</span
                ></small
              >
            </button>
          </div>
          <article v-if="chosenVersion" class="card">
            <h3>{{ t("sanitizeRules.selected") }} {{ chosenVersion.id }}</h3>
            <p>{{ t("sanitizeRules.compare") }}</p>
            <ul v-if="versionDiff.length">
              <li v-for="row in versionDiff" :key="row.id">
                <code>{{ row.id }}</code
                >: {{ labels[row.change] }}
              </li>
            </ul>
            <p v-else>{{ t("sanitizeRules.noChanges") }}</p>
            <details>
              <summary>{{ t("sanitizeRules.versionDetail") }}</summary>
              <pre>{{ JSON.stringify(chosenVersion.config, null, 2) }}</pre>
            </details>
            <button
              class="btn danger"
              :disabled="busy || !head || chosenVersion.id === head.version"
              data-test="open-rollback"
              @click="rollbackOpen = true"
            >
              {{ t("sanitizeRules.rollback") }}
            </button>
            <div
              v-if="rollbackOpen"
              class="rollback-confirm"
              role="group"
              :aria-label="t('sanitizeRules.rollbackConfirm')"
            >
              <p>{{ t("sanitizeRules.rollbackConfirm") }}</p>
              <label
                >{{ t("sanitizeRules.comment")
                }}<input
                  v-model="rollbackComment"
                  data-test="rollback-comment"
                  maxlength="2000"
                  :disabled="busy" /></label
              ><button
                class="btn danger"
                :disabled="busy || !rollbackComment.trim()"
                data-test="confirm-rollback"
                @click="rollback"
              >
                {{ t("sanitizeRules.confirm") }}</button
              ><button
                class="btn"
                :disabled="busy"
                @click="rollbackOpen = false"
              >
                {{ t("sanitizeRules.cancel") }}
              </button>
            </div>
          </article>
        </div>
      </section>
      <section v-if="tab === 'instances'" :aria-busy="instanceLoading">
        <div class="toolbar">
          <strong>{{ t("sanitizeRules.target") }} {{ targetVersion }}</strong
          ><button
            class="btn"
            :disabled="instanceLoading"
            @click="loadInstances"
          >
            {{ t("sanitizeRules.refresh") }}
          </button>
        </div>
        <p v-if="!instances.length && !instanceError">
          {{ t("sanitizeRules.noInstances") }}
        </p>
        <article
          v-for="instance in instances"
          :key="instance.instance_id"
          class="card"
        >
          <div class="toolbar">
            <code>{{ instance.instance_id }}</code
            ><strong>v{{ instance.version }}</strong
            ><span v-if="instance.version === 0">{{
              t("sanitizeRules.fallback")
            }}</span
            ><span v-else>{{
              t(
                instance.version === targetVersion
                  ? "sanitizeRules.applied"
                  : "sanitizeRules.waiting",
              )
            }}</span
            ><strong v-if="stale(instance)" class="warning">{{
              t("sanitizeRules.stale")
            }}</strong>
          </div>
          <p>
            <small
              >{{ t("sanitizeRules.digest") }}:
              <code>{{ instance.config_digest }}</code></small
            >
          </p>
          <p>
            {{ t("sanitizeRules.lastSeen") }}: {{ date(instance.last_seen) }}
          </p>
          <p>
            {{ t("sanitizeRules.lastRefresh") }}:
            {{ date(instance.last_refresh) }}
          </p>
          <p v-if="instance.error" role="alert" class="error-banner">
            {{ t("sanitizeRules.lastError") }}: {{ instance.error }}
          </p>
        </article>
      </section>
      <section v-if="tab === 'parameters'" class="card">
        <p v-if="parameterError" role="alert" class="error-banner">
          {{ parameterError }}
        </p>
        <form class="parameter-form" @submit.prevent="saveParams">
          <label
            >{{ t("sanitizeRules.refreshSeconds")
            }}<input
              v-model.number="params.refresh"
              data-test="parameter-refresh"
              type="number"
              min="1"
              max="300"
              required
              :disabled="paramsBusy || paramsLoading || !paramsLoaded" /></label
          ><label class="check"
            ><input
              v-model="params.audit"
              data-test="parameter-audit"
              type="checkbox"
              :disabled="paramsBusy || paramsLoading || !paramsLoaded"
            />{{ t("sanitizeRules.auditEnabled") }}</label
          >
          <label
            >{{ t("sanitizeRules.eventLimit")
            }}<input
              v-model.number="params.limit"
              data-test="parameter-limit"
              type="number"
              min="100"
              max="100000"
              required
              :disabled="paramsBusy || paramsLoading || !paramsLoaded"
          /></label>
          <div class="toolbar">
            <button
              class="btn btn-primary"
              :disabled="
                paramsBusy || paramsLoading || !paramsLoaded || !!parameterError
              "
            >
              {{ t("sanitizeRules.saveParameters") }}</button
            ><button
              type="button"
              class="btn"
              :disabled="paramsBusy || paramsLoading"
              @click="loadParams"
            >
              {{ t("sanitizeRules.refresh") }}
            </button>
          </div>
        </form>
      </section>
    </template>
  </main>
</template>
<style scoped>
.sanitize-rules-view {
  padding: 20px;
  min-width: 0;
  color: var(--text-primary);
}
.provenance,
.toolbar,
.tabs {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.provenance {
  border: 1px solid var(--border);
  background: var(--bg-secondary);
  padding: 12px;
  margin-bottom: 16px;
  font-variant-numeric: tabular-nums;
}
.provenance small {
  margin-inline-start: auto;
}
.tabs {
  border-bottom: 1px solid var(--border);
  margin: 16px 0;
  gap: 0;
}
.tab {
  background: transparent;
  border: 0;
  border-bottom: 2px solid transparent;
  padding: 12px 16px;
  color: var(--muted);
  cursor: pointer;
}
.tab.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
}
.toolbar {
  margin-bottom: 12px;
}
.toolbar label {
  margin: 0;
}
.card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
  background: var(--bg-card, var(--bg-secondary));
}
.rule-workspace {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(300px, 430px);
  gap: 18px;
  align-items: start;
}
.rule-workspace:not(:has(.editor)) {
  grid-template-columns: minmax(0, 1fr);
}
.table-scroll {
  overflow-x: auto;
}
table {
  width: 100%;
  border-collapse: collapse;
}
th,
td {
  text-align: start;
  padding: 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
th {
  font-size: 12px;
  color: var(--muted);
}
tr.selected {
  background: var(--bg-hover);
}
small {
  display: block;
  color: var(--muted);
  overflow-wrap: anywhere;
}
code,
pre,
.json-editor {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
}
code {
  overflow-wrap: anywhere;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
label {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 13px;
  margin-bottom: 12px;
}
.check {
  display: inline-flex;
  flex-direction: row;
  align-items: center;
  margin-inline-end: 12px;
}
.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0 12px;
}
input:not([type="checkbox"]),
textarea,
select {
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--bg-input, var(--bg));
  color: var(--text-primary);
  padding: 8px;
  min-width: 0;
  max-width: 100%;
  font: inherit;
}
textarea {
  width: 100%;
  box-sizing: border-box;
}
.json-editor {
  width: 100%;
  margin: 12px 0;
}
input[type="checkbox"] {
  accent-color: var(--accent);
  width: 16px;
  height: 16px;
}
.btn {
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--bg-secondary);
  color: var(--text-primary);
  cursor: pointer;
  text-decoration: none;
  display: inline-block;
}
.btn-primary {
  background: var(--accent);
  color: var(--on-primary);
  border-color: var(--accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.danger {
  color: var(--danger);
}
button:focus-visible,
input:focus-visible,
select:focus-visible,
textarea:focus-visible,
summary:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.error-banner {
  padding: 12px;
  border: 1px solid var(--danger);
  background: var(--bg-secondary);
  color: var(--danger);
}
.notice {
  padding: 12px;
  border: 1px solid var(--accent);
  background: var(--bg-secondary);
}
.warning {
  color: var(--warning);
}
.muted {
  color: var(--muted);
  font-size: 12px;
}
fieldset {
  border: 1px solid var(--border);
  margin: 0 0 12px;
  padding: 10px;
}
legend {
  font-size: 12px;
}
h3,
h4 {
  margin: 0 0 12px;
}
summary {
  cursor: pointer;
  padding: 10px 0;
}
.sample {
  border-bottom: 1px dashed var(--border);
  padding: 10px 0;
  margin-bottom: 12px;
}
.publish {
  margin-top: 16px;
}
.event > code {
  display: block;
  margin-bottom: 10px;
}
.event > small {
  margin-bottom: 12px;
}
.history-layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 16px;
}
.version-row {
  display: block;
  width: 100%;
  text-align: start;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  color: var(--text-primary);
  padding: 12px;
  margin-bottom: 8px;
  cursor: pointer;
}
.rollback-confirm {
  margin-top: 12px;
  border: 1px solid var(--warning);
  padding: 12px;
}
.parameter-form {
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.parameter-form .toolbar {
  grid-column: 1/-1;
}
@media (max-width: 1024px) {
  .rule-workspace,
  .history-layout {
    grid-template-columns: 1fr;
  }
  .sanitize-rules-view {
    padding: 12px;
  }
  .provenance small {
    margin-inline-start: 0;
  }
  .parameter-form {
    grid-template-columns: 1fr;
  }
}
@media (prefers-reduced-motion: reduce) {
  * {
    scroll-behavior: auto;
  }
}
.event-note {
  color: var(--muted);
  font-size: 13px;
  margin: 0 0 12px;
}
.event-meta {
  margin: 0 0 8px;
  font-size: 13px;
  color: var(--text-secondary, var(--muted));
}
.matched-rule {
  margin: 10px 0;
}
.matched-rule__body {
  margin-top: 8px;
  padding: 10px;
  border: 1px dashed var(--border);
  border-radius: 6px;
  background: var(--bg-secondary);
}
.matched-pattern {
  display: block;
  margin-top: 4px;
  white-space: pre-wrap;
  word-break: break-all;
}
.matched-samples {
  margin: 8px 0 0;
  padding-inline-start: 18px;
}
.matched-samples li {
  margin-bottom: 4px;
}
</style>
