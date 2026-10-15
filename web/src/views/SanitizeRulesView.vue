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
</script>
<template>

</template>
<style scoped></style>
