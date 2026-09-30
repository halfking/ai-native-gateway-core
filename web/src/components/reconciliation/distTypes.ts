// distTypes.ts — distribution table row shape shared by the builder and the table.

export type BarTone = 'primary' | 'success' | 'purple' | 'cyan'
export type CellTone = 'ok' | 'warn' | 'bad'

export interface ReasonChip {
  code: string
  count: number
}

export interface DistCell {
  text: string
  sub?: string
  tone?: CellTone
}

export interface DistRow {
  key: string
  name: string
  sub?: string
  /** Bar width 0–100, relative to the current metric's max row. */
  pct: number
  tone?: BarTone
  cells: DistCell[]
  reasons?: ReasonChip[]
}
