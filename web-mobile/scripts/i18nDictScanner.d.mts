export interface DictScan {
  /** 键路径 → 字面值 */
  out: Map<string, string>
  /** 重复键报告：`键路径（第 N 行与第 M 行）` */
  duplicates: string[]
}

/**
 * 从词典源码（`.ts`）里抽出「键路径 → 字面值」。
 *
 * 刻意不做 import：调用方是 build 前置里的 `verify-i18n-parity.mjs`，
 * 那时没有编译链。正确性由 `src/i18n/dictParserParity.spec.ts`
 * 与真实对象逐键对账来保证。
 */
export function parseDictSource(src: string): DictScan
