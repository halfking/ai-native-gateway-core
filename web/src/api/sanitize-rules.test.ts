import { beforeEach, describe, expect, it, vi } from "vitest";
const request = vi.hoisted(() => vi.fn());
vi.mock("./_core", () => ({ req: request }));
import {
  getSanitizeRules,
  validateSanitizeRules,
  publishSanitizeRules,
  reviewSanitizeRuleEvent,
  listSanitizeRuleEvents,
  listSanitizeRuleInstances,
  rollbackSanitizeRules,
  type RuleConfig,
} from "./sanitize-rules";
const config: RuleConfig = { rules: [] };
describe("sanitize rule API contracts", () => {
  beforeEach(() => request.mockReset());
  it("keeps CAS and review references in the publish request", async () => {
    request.mockResolvedValue({ id: 3 });
    await publishSanitizeRules({
      base_version: 2,
      config,
      comment: "scoped fix",
      review_event_ids: [5],
    });
    expect(request).toHaveBeenCalledWith(
      "POST",
      "/api/admin/sanitize-rules/publish",
      { base_version: 2, config, comment: "scoped fix", review_event_ids: [5] },
    );
  });
  it("validates a direct Config body and forwards cancellation on polling", async () => {
    const controller = new AbortController();
    request.mockResolvedValue({ version: 1, config, config_digest: "digest" });
    await getSanitizeRules({ signal: controller.signal });
    expect(request).toHaveBeenLastCalledWith(
      "GET",
      "/api/admin/sanitize-rules",
      undefined,
      { signal: controller.signal },
    );
    await validateSanitizeRules(config);
    expect(request).toHaveBeenLastCalledWith(
      "POST",
      "/api/admin/sanitize-rules/validate",
      config,
    );
    await listSanitizeRuleInstances({ signal: controller.signal });
    expect(request).toHaveBeenLastCalledWith(
      "GET",
      "/api/admin/sanitize-rules/instances",
      undefined,
      { signal: controller.signal },
    );
  });
  it("does not accept a caller tenant parameter in event/review APIs", async () => {
    request.mockResolvedValue({ events: [] });
    await listSanitizeRuleEvents({ action: "exempted", status: "pending" });
    expect(request).toHaveBeenLastCalledWith(
      "GET",
      "/api/admin/sanitize-rules/events?action=exempted&status=pending&limit=100",
      undefined,
      undefined,
    );
    await reviewSanitizeRuleEvent(4, {
      status: "false_positive",
      comment: "reviewed",
    });
    expect(request).toHaveBeenLastCalledWith(
      "POST",
      "/api/admin/sanitize-rules/events/4/review",
      { status: "false_positive", comment: "reviewed" },
    );
  });
  it("creates a new rollback version with the current base and reason", async () => {
    await rollbackSanitizeRules({
      base_version: 3,
      target_version: 1,
      comment: "restore",
    });
    expect(request).toHaveBeenLastCalledWith(
      "POST",
      "/api/admin/sanitize-rules/rollback",
      { base_version: 3, target_version: 1, comment: "restore" },
    );
  });
});

it("guards editor JSON before rendering and preserves explicit exemption metadata", async () => {
  const { isRuleConfig, RULE_KINDS } = await import("./sanitize-rules");
  expect(isRuleConfig({ rules: [null] })).toBe(false);
  expect(isRuleConfig({ rules: [{ id: "x", scopes: "egress" }] })).toBe(false);
  expect(RULE_KINDS).toContain("review_combination");
  expect(
    isRuleConfig({
      rules: [
        {
          id: "x",
          name: "X",
          type: "secret",
          kind: "pattern",
          pattern: "secret",
          group: 0,
          priority: 0,
          enabled: true,
          scopes: ["egress"],
          action: "block",
          exemptions: ["identity"],
        },
      ],
    }),
  ).toBe(true);
});
