import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";
import en from "../locales/en-US";
import zh from "../locales/zh-CN";
import tw from "../locales/zh-TW";
import ja from "../locales/ja-JP";
import de from "../locales/de-DE";
import fr from "../locales/fr-FR";
import es from "../locales/es-ES";
import ar from "../locales/ar-SA";

const bundles: Record<string, Record<string, unknown>> = {
  en,
  "zh-CN": zh,
  "zh-TW": tw,
  ja,
  de,
  fr,
  es,
  ar,
};
const expectedKeys = Object.keys(en.sanitizeRules).sort();

describe("sanitize rules in actual bundled and lazy locale exports", () => {
  it("pins the complete source namespace", () =>
    // 2026-10-20：+2（perMode / perModeHint）—— 按模式启停矩阵的表头与说明。
    // 计数只许增：每加一个键都要同步 8 个语种 + 这里。
    expect(expectedKeys).toHaveLength(119));
  for (const [locale, bundle] of Object.entries(bundles)) {
    it(`${locale} exports and resolves every rule-management message`, () => {
      const namespace = bundle.sanitizeRules as
        | Record<string, string>
        | undefined;
      expect(
        namespace,
        `missing sanitizeRules namespace in actual ${locale} index`,
      ).toBeDefined();
      expect(Object.keys(namespace ?? {}).sort()).toEqual(expectedKeys);
      const i18n = createI18n({
        legacy: false,
        locale,
        fallbackLocale: false,
        messages: { [locale]: { sanitizeRules: namespace ?? {} } },
        missingWarn: false,
        fallbackWarn: false,
      });
      for (const key of expectedKeys) {
        expect(namespace?.[key]).toBeTruthy();
        expect(i18n.global.t(`sanitizeRules.${key}`)).toBe(namespace?.[key]);
      }
    });
  }
});
