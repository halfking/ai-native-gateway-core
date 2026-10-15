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
</script>
<template>

</template>
<style scoped></style>
