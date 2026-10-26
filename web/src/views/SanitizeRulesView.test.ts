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
          type: "secret",
          field: "pattern",
          sample_masked: "[REDACTED:secret] field=pattern phase=input action=mask",
          count: 2,
          first_seen: "2026-10-15T00:00:00Z",
          last_seen: "2026-10-15T00:00:00Z",
          matched_rule: {
            id: "secret",
            name: "Secret rule",
            type: "secret",
            kind: "pattern",
            pattern: "secret=(\\w+)",
            scopes: ["input"],
            action: "mask",
            enabled: true,
            samples: [{ text: "secret=example", match: true }],
          },
        },
      ],
    });
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-events"]').trigger("click");
    await flushPromises();
    expect(w.text()).toContain("secret");
    expect(w.text()).toContain("pattern");
    await w.get('[data-test="toggle-matched-7"]').trigger("click");
    expect(w.get('[data-test="matched-body-7"]').text()).toContain(
      "secret=(\\w+)",
    );
    // pending 即可编辑/起草，不必先复核
    expect(w.find('[data-test="edit-event-7"]').exists()).toBe(true);
    expect(w.find('[data-test="draft-event-7"]').exists()).toBe(true);
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
  it("saves the three real module parameter keys", async () => {
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-parameters"]').trigger("click");
    await w.get('[data-test="parameter-refresh"]').setValue(17);
    await w.get('[data-test="parameter-audit"]').setValue(false);
    await w.get('[data-test="parameter-limit"]').setValue(345);
    await w.get("form.parameter-form").trigger("submit");
    await flushPromises();
    expect(mocks.updateSetting.mock.calls).toEqual([
      ["sanitize.rules.refresh_interval_seconds", { value: 17 }],
      ["sanitize.rules.audit_enabled", { value: false }],
      ["sanitize.rules.event_limit", { value: 345 }],
    ]);
    expect(w.text()).toContain(messages.parametersSaved);
  });
  it("requires rollback confirmation and a reason before creating a new version", async () => {
    mocks.current.mockResolvedValue({ ...structuredClone(head), version: 3 });
    mocks.versions.mockResolvedValue({
      versions: [
        {
          id: 1,
          parent_id: 0,
          config: head.config,
          actor: "admin",
          comment: "initial",
          created_at: "2026-10-15T00:00:00Z",
        },
      ],
    });
    mocks.rollback.mockResolvedValue({ id: 4, config: head.config });
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-versions"]').trigger("click");
    await flushPromises();
    await w.get("button.version-row").trigger("click");
    // Loading the editor materializes optional empty lists; that is not a policy change.
    expect(w.text()).toContain(messages.noChanges);
    await w.get('[data-test="open-rollback"]').trigger("click");
    expect(mocks.rollback).not.toHaveBeenCalled();
    expect(
      w.get('[data-test="confirm-rollback"]').attributes("disabled"),
    ).toBeDefined();
    await w
      .get('[data-test="rollback-comment"]')
      .setValue("restore verified rules");
    await w.get('[data-test="confirm-rollback"]').trigger("click");
    await flushPromises();
    expect(mocks.rollback).toHaveBeenCalledWith({
      base_version: 3,
      target_version: 1,
      comment: "restore verified rules",
    });
  });
  it("edits the actual matched rule without changing its global scope", async () => {
    mocks.events.mockResolvedValue({ events: [{
      id: 8, tenant_id: "tenant-a", rule_id: "secret", version: 1,
      phase: "input", action: "mask", status: "false_positive",
      sample_masked: "[REDACTED:secret]", count: 1,
      first_seen: "", last_seen: "",
    }] });
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-events"]').trigger("click");
    await flushPromises();
    await w.get('[data-test="edit-event-8"]').trigger("click");
    expect(w.text()).toContain(messages.editMatchedHint);
    expect(w.findAll('[data-test="rule-row"]')).toHaveLength(1);
    expect(w.get('[data-test="rule-tenant"]').element).toHaveProperty("value", "");
    await w.get('[data-test="validate"]').trigger("click");
    await flushPromises();
    await w.get('[data-test="publish-comment"]').setValue("fix actual rule");
    await w.get('[data-test="publish"]').trigger("click");
    await flushPromises();
    expect(mocks.publish).toHaveBeenCalledWith(expect.objectContaining({
      review_event_ids: [8], config: { rules: [expect.objectContaining({
        id: "secret", scopes: ["input"],
      })] },
    }));
  });
  it("still reports real rule changes when optional lists are normalized", async () => {
    mocks.versions.mockResolvedValue({ versions: [{
      id: 1, parent_id: 0, actor: "admin", comment: "old", created_at: "",
      config: { rules: [{ ...rule, pattern: "different-pattern" }] },
    }] });
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-versions"]').trigger("click");
    await flushPromises();
    await w.get("button.version-row").trigger("click");
    expect(w.text()).toContain(messages.changed);
    expect(w.text()).not.toContain(messages.noChanges);
  });
  it("keeps unapplied JSON text when the user cancels reloading", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const w = open();
    await flushPromises();
    const json = w.get("textarea.json-editor");
    const pending = '{"rules":[]}';
    await json.setValue(pending);
    await w.get(".app-page-header button").trigger("click");
    await flushPromises();
    expect(confirm).toHaveBeenCalledWith(messages.discard);
    expect(mocks.current).toHaveBeenCalledTimes(1);
    expect(json.element).toHaveProperty("value", pending);
  });
  it("serializes deferred parameter loads and disables editing until the active load finishes", async () => {
    const pending: {
      key: string;
      resolve: (value: { value: number | boolean }) => void;
    }[] = [];
    mocks.getSetting.mockImplementation(
      (key: string) => new Promise((resolve) => pending.push({ key, resolve })),
    );
    const w = open();
    await flushPromises();
    await w.get('[data-test="tab-parameters"]').trigger("click");
    const reload = w.get('form.parameter-form button[type="button"]');
    expect(reload.attributes("disabled")).toBeDefined();
    expect(
      w.get('[data-test="parameter-refresh"]').attributes("disabled"),
    ).toBeDefined();
    await reload.trigger("click");
    expect(mocks.getSetting).toHaveBeenCalledTimes(3);
    for (const request of pending.splice(0)) {
      request.resolve({
        value: request.key.endsWith("audit_enabled")
          ? true
          : request.key.endsWith("event_limit")
            ? 10000
            : 5,
      });
    }
    await flushPromises();
    await reload.trigger("click");
    await reload.trigger("click");
    expect(mocks.getSetting).toHaveBeenCalledTimes(6);
    expect(
      w.get('[data-test="parameter-refresh"]').attributes("disabled"),
    ).toBeDefined();
    for (const request of pending.splice(0)) {
      request.resolve({
        value: request.key.endsWith("audit_enabled")
          ? false
          : request.key.endsWith("event_limit")
            ? 321
            : 17,
      });
    }
    await flushPromises();
    expect(w.get('[data-test="parameter-refresh"]').element).toHaveProperty(
      "value",
      "17",
    );
    expect(reload.attributes("disabled")).toBeUndefined();
  });
  it("validates the request snapshot and never certifies a draft changed while the request is in flight", async () => {
    let resolve!: (value: { valid: boolean; config_digest: string }) => void;
    mocks.validate.mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const w = open();
    await flushPromises();
    await w.get('[data-test="edit-rule"]').trigger("click");
    await w.get('[data-test="validate"]').trigger("click");
    const input = w.get('[data-test="rule-name"]').element as HTMLInputElement;
    input.value = "Changed by another editor entry";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    resolve({ valid: true, config_digest: "old-draft" });
    await flushPromises();
    expect(mocks.validate).toHaveBeenCalledWith(
      expect.objectContaining({
        rules: [expect.objectContaining({ name: "Secret rule" })],
      }),
    );
    expect(w.get('[data-test="publish"]').attributes("disabled")).toBeDefined();
    expect(w.text()).not.toContain(messages.valid);
  });
});

// ─────────────────────────────────────────────────────────────────────────
// ★ 按模式启停矩阵（2026-10-20 审计轮补齐）
//
// 需求原话：「脱敏需要有个设置界面，可以根据模式来设置匹配规则，启用或停止相关的规则。」
//
// 运行期的判定在 domains/sanitizerules/model.go 的 Applies(phase, tenant, tool, field)：
//
//     r.Enabled && scope 命中 && tenant 命中
//
// ⇒ 「这条规则在某个模式上是否生效」= **全局开关 ∧ scopes 含该模式**。
// 本组判据钉的就是这个语义：矩阵的勾选状态必须与运行期判定**同源**，
// 而不是「scopes 里有没有这个 phase」这种看似等价、实则差一个 Enabled 的近似。
// ─────────────────────────────────────────────────────────────────────────
describe("按模式启停矩阵（与运行期 Applies() 同源）", () => {
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
    mocks.validate.mockResolvedValue({ valid: true, config_digest: "validated" });
    mocks.publish.mockResolvedValue({ id: 2, config: head.config });
    mocks.review.mockResolvedValue({});
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
  });

  it("每条规则给出 input/output/egress 三个模式开关", async () => {
    const w = open();
    await flushPromises();
    // find() 才有 exists()；get() 找不到会直接抛（那本身就是正向断言）。
    const matrix = w.find('[data-test="rule-mode-matrix"]');
    expect(matrix.exists()).toBe(true);
    for (const phase of ["input", "output", "egress"]) {
      const box = w.get(`[data-test="rule-mode-${phase}"]`);
      expect((box.element as HTMLInputElement).type).toBe("checkbox");
    }
  });

  it("勾选状态 = 全局启用 ∧ scopes 含该模式（不是只看 scopes）", async () => {
    // 规则：enabled=true, scopes=["input"] ⇒ 只有 input 亮。
    const w = open();
    await flushPromises();
    const input = w.get('[data-test="rule-mode-input"]');
    const output = w.get('[data-test="rule-mode-output"]');
    const egress = w.get('[data-test="rule-mode-egress"]');
    expect((input.element as HTMLInputElement).checked).toBe(true);
    expect((output.element as HTMLInputElement).checked).toBe(false);
    expect((egress.element as HTMLInputElement).checked).toBe(false);
  });

  it("全局停用时，三个模式开关全部显示为停用（口径与运行期一致）", async () => {
    const off = structuredClone(head);
    off.config.rules[0].enabled = false;
    off.config.rules[0].scopes = ["input", "output"];
    mocks.current.mockResolvedValue(off);
    const w = open();
    await flushPromises();
    expect((w.get('[data-test="rule-mode-input"]').element as HTMLInputElement).checked).toBe(false);
    expect((w.get('[data-test="rule-mode-output"]').element as HTMLInputElement).checked).toBe(false);
  });

  it("取消某个模式 ⇒ 该模式从 scopes 里摘掉（判据看**发出去的草稿**，不看组件内部）", async () => {
    // ⚠ 判据纪律：<script setup> 的内部变量不挂在 vm 上，写 `w.vm.draft` 只会拿到
    //   undefined，再套个 `if (draftScopes)` 就成了**恒不执行**的假绿。
    //   可观测的契约是「validate 发出去的草稿」——那里带的就是整份草稿。
    const w = open();
    await flushPromises();
    const input = w.get('[data-test="rule-mode-input"]');
    await input.setValue(false);
    await flushPromises();
    await w.get('[data-test="validate"]').trigger("click");
    await flushPromises();
    expect(mocks.validate).toHaveBeenCalledTimes(1);
    const payload = mocks.validate.mock.calls[0][0] as any;
    expect(payload.rules[0].scopes).toEqual([]);
    // 矩阵的勾选状态也跟着变（这是「所见即所存」的一半）
    expect((w.get('[data-test="rule-mode-input"]').element as HTMLInputElement).checked).toBe(false);
  });

  it("勾上某个模式 ⇒ scopes 加上该模式，并把全局开关带上（否则运行期仍然不生效）", async () => {
    const off = structuredClone(head);
    off.config.rules[0].enabled = false;
    mocks.current.mockResolvedValue(off);
    const w = open();
    await flushPromises();
    const output = w.get('[data-test="rule-mode-output"]');
    expect((output.element as HTMLInputElement).checked).toBe(false);
    await output.setValue(true);
    await flushPromises();
    await w.get('[data-test="validate"]').trigger("click");
    await flushPromises();
    const payload = mocks.validate.mock.calls[0][0] as any;
    expect(payload.rules[0].enabled).toBe(true);
    expect(payload.rules[0].scopes).toContain("output");
  });
});
