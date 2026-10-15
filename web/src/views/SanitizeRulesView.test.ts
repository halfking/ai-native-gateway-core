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
describe("managed rule workflows", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.super.mockReturnValue(true);
    mocks.tenant.mockReturnValue(false);
    mocks.current.mockResolvedValue(structuredClone(head));
    mocks.events.mockResolvedValue({ events: [] });
    mocks.versions.mockResolvedValue({ versions: [] });
    mocks.instances.mockResolvedValue({ target_version: 1, instances: [] });
    mocks.getSetting.mockResolvedValue({ value: 5 });
    mocks.updateSetting.mockResolvedValue({});
    mocks.validate.mockResolvedValue({
      valid: true,
      config_digest: "validated",
    });
    mocks.publish.mockResolvedValue({ id: 2, config: head.config });
    mocks.review.mockResolvedValue({});
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.useRealTimers();
  });
  it("loads real rules and can add/delete/edit without changing the published baseline", async () => {
    const w = open();
    await flushPromises();
    expect(w.text()).toContain("Secret rule");
    await w.get('[data-test="add-rule"]').trigger("click");
    expect(w.findAll('[data-test="rule-row"]')).toHaveLength(2);
    await w.get('[data-test="rule-name"]').setValue("New private rule");
    await w.get('[data-test="delete-rule"]').trigger("click");
    expect(w.findAll('[data-test="rule-row"]')).toHaveLength(1);
    expect(mocks.publish).not.toHaveBeenCalled();
  });
  it("validates sample expectations and preserves drafts on CAS conflicts without retry", async () => {
    const w = open();
    await flushPromises();
    await w.get('[data-test="edit-rule"]').trigger("click");
    await w.get('[data-test="rule-name"]').setValue("My changed rule");
    await w.get('[data-test="validate"]').trigger("click");
    await flushPromises();
    expect(mocks.validate).toHaveBeenCalledWith(
      expect.objectContaining({
        rules: [expect.objectContaining({ name: "My changed rule" })],
      }),
    );
    mocks.publish.mockRejectedValue(
      Object.assign(new Error("changed"), { status: 409 }),
    );
    await w.get('[data-test="publish-comment"]').setValue("reason");
    await w.get('[data-test="publish"]').trigger("click");
    await flushPromises();
    expect(mocks.publish).toHaveBeenCalledTimes(1);
    expect(w.get('[data-test="rule-name"]').element).toHaveProperty(
      "value",
      "My changed rule",
    );
    expect(w.text()).toContain(messages.conflict);
  });
  it("reviews a tenant event and creates a scope-limited linked draft", async () => {
    mocks.events.mockResolvedValue({
      events: [
        {
          id: 7,
          tenant_id: "tenant-a",
          rule_id: "secret",
          version: 1,
          phase: "input",
          action: "mask",
          status: "pending",
          sample_masked: "[REDACTED:secret]",
          count: 2,
          first_seen: "2026-10-15T00:00:00Z",
          last_seen: "2026-10-15T00:00:00Z",
        },
      ],
    });
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-events"]').trigger("click");
    await flushPromises();
    await w
      .get('[data-test="review-comment-7"]')
      .setValue("limited false positive");
    await w.get('[data-test="review-false_positive-7"]').trigger("click");
    await flushPromises();
    expect(mocks.review).toHaveBeenCalledWith(7, {
      status: "false_positive",
      comment: "limited false positive",
    });
    await w.get('[data-test="draft-event-7"]').trigger("click");
    expect(w.get('[data-test="rule-tenant"]').element).toHaveProperty(
      "value",
      "tenant-a",
    );
    expect(w.text()).toContain("7");
    await w.get('[data-test="validate"]').trigger("click");
    await flushPromises();
    await w.get('[data-test="publish-comment"]').setValue("limit rule");
    await w.get('[data-test="publish"]').trigger("click");
    await flushPromises();
    expect(mocks.publish).toHaveBeenCalledWith(
      expect.objectContaining({
        base_version: 1,
        review_event_ids: [7],
        config: expect.objectContaining({
          rules: expect.arrayContaining([
            expect.objectContaining({ tenant_id: "tenant-a" }),
          ]),
        }),
      }),
    );
  });
  it("stops instance polling on unmount and reports failures instead of false convergence", async () => {
    vi.useFakeTimers();
    mocks.instances.mockRejectedValue(new Error("offline"));
    const w = open();
    await flushPromises();
    expect(w.text()).toContain("offline");
    w.unmount();
    wrapper = undefined;
    const count = mocks.instances.mock.calls.length;
    await vi.advanceTimersByTimeAsync(20000);
    expect(mocks.instances).toHaveBeenCalledTimes(count);
  });
  it("tenant administrators only fetch review data", async () => {
    mocks.super.mockReturnValue(false);
    mocks.tenant.mockReturnValue(true);
    const w = open();
    await flushPromises();
    expect(mocks.current).not.toHaveBeenCalled();
    expect(mocks.instances).not.toHaveBeenCalled();
    expect(mocks.events).toHaveBeenCalled();
    expect(w.find('[data-test="tab-rules"]').exists()).toBe(false);
  });
});
describe("editor and polling boundaries", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.super.mockReturnValue(true);
    mocks.tenant.mockReturnValue(false);
    mocks.current.mockResolvedValue(structuredClone(head));
    mocks.events.mockResolvedValue({ events: [] });
    mocks.versions.mockResolvedValue({ versions: [] });
    mocks.instances.mockResolvedValue({ target_version: 1, instances: [] });
    mocks.getSetting.mockImplementation((key: string) =>
      Promise.resolve({
        value: key.endsWith("event_limit")
          ? 10000
          : key.endsWith("audit_enabled")
            ? true
            : 5,
      }),
    );
    mocks.updateSetting.mockResolvedValue({});
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.restoreAllMocks();
  });
  it("cancels an in-flight instance request when the page is hidden", async () => {
    let signal: AbortSignal | undefined;
    mocks.instances.mockImplementation(
      (options: { signal: AbortSignal }) =>
        new Promise((_resolve, reject) => {
          signal = options.signal;
          signal.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
    open();
    await flushPromises();
    expect(signal?.aborted).toBe(false);
    hidden.mockReturnValue(true);
    document.dispatchEvent(new Event("visibilitychange"));
    await flushPromises();
    expect(signal?.aborted).toBe(true);
  });
  it("does not let initial defaults overwrite parameters before they load", async () => {
    mocks.getSetting.mockReturnValue(new Promise(() => {}));
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-parameters"]').trigger("click");
    expect(w.find("button.btn-primary").attributes("disabled")).toBeDefined();
  });
  it("rejects JSON that could crash rule rendering while keeping the existing draft", async () => {
    const w = open();
    await flushPromises();
    const area = w.get("textarea.json-editor");
    await area.setValue('{"rules":[null]}');
    await w.find("details button").trigger("click");
    await flushPromises();
    expect(w.text()).toContain(messages.invalidJson);
    expect(w.findAll('[data-test="rule-row"]')).toHaveLength(1);
  });
});
