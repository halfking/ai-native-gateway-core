import { mount, flushPromises } from "@vue/test-utils";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { createI18n } from "vue-i18n";
const mocks = vi.hoisted(() => ({
  current: vi.fn(),
  validate: vi.fn(),
  publish: vi.fn(),
  events: vi.fn(),
  versions: vi.fn(),
  instances: vi.fn(),
  review: vi.fn(),
  rollback: vi.fn(),
  getSetting: vi.fn(),
  updateSetting: vi.fn(),
  super: vi.fn(() => true),
  tenant: vi.fn(() => false),
}));
vi.mock("../api/sanitize-rules", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  getSanitizeRules: mocks.current,
  validateSanitizeRules: mocks.validate,
  publishSanitizeRules: mocks.publish,
  listSanitizeRuleEvents: mocks.events,
  listSanitizeRuleVersions: mocks.versions,
  listSanitizeRuleInstances: mocks.instances,
  reviewSanitizeRuleEvent: mocks.review,
  rollbackSanitizeRules: mocks.rollback,
}));
vi.mock("../api/settings", () => ({
  getSetting: mocks.getSetting,
  updateSetting: mocks.updateSetting,
}));
vi.mock("../store", () => ({
  isSuperAdmin: mocks.super,
  isTenantAdmin: mocks.tenant,
}));
import SanitizeRulesView from "./SanitizeRulesView.vue";
import messages from "../locales/en-US/sanitizeRules";
const rule = {
  id: "secret",
  name: "Secret rule",
  type: "secret",
  kind: "pattern",
  pattern: "secret=(\\w+)",
  group: 1,
  scopes: ["input"],
  action: "mask",
  enabled: true,
  priority: 10,
  samples: [{ text: "secret=example", match: true }],
};
const head = {
  version: 1,
  config: { rules: [rule] },
  config_digest: "digest1",
  status: { instance_id: "node", version: 1, error: "" },
};
let wrapper: ReturnType<typeof mount> | undefined;
function open() {
  wrapper = mount(SanitizeRulesView, {
    global: {
      plugins: [
        createI18n({
          legacy: false,
          locale: "en",
          messages: { en: { sanitizeRules: messages } },
        }),
      ],
      stubs: { RouterLink: { template: "<a><slot /></a>" } },
    },
  });
  return wrapper;
}
