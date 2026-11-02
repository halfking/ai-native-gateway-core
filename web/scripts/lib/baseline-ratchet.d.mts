export declare function localDate(d?: Date): string

export declare function mergeRatchetBaseline(
  prevCounts?: Record<string, number>,
  measuredCounts?: Record<string, number>,
): {
  next: Record<string, number>
  keptHigher: Array<{ page: string; measured: number; kept: number }>
  preserved: string[]
  added: string[]
}
