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
                    <th>{{ t("sanitizeRules.scopes") }}</th>
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
                    <td>{{ rule.scopes.join(", ") }}</td>
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
          </details> </template
        >
      </section>
    </template>
  </main>
</template>
<style scoped></style>
