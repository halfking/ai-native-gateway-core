export declare const MIX_BLACKWHITE_RE: RegExp

export declare function exemptColorMixBlacks(
  line: string,
  state?: { inMix: boolean; depth: number },
): string

export declare function stripVarFallbacks(line: string): {
  line: string
  fallbacks: string[]
}
