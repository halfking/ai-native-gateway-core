/**
 * Format bytes to human-readable string (B, KB, MB, GB, TB).
 *
 * 2026-07-25: Added for dashboard body size statistics display.
 * 2026-07-26 (audit): Tightened input handling — now treats undefined,
 * null, NaN, Infinity, negative, and zero all as "0 B". Also caps the
 * unit index so absurdly large inputs don't return undefined (e.g.
 * 1 PiB → '1024.00 TB' instead of `'undefined'`).
 *
 * @param bytes - Non-negative integer byte count, or null/undefined.
 * @returns Human-readable size string (e.g. '1.50 MB').
 */
export function formatBytes(bytes?: number | null): string {
  // Treat every "missing" case uniformly so the dashboard shows '0 B'
  // rather than 'NaN undefined' when the API has not yet reported any
  // data for the field.
  if (bytes == null || !Number.isFinite(bytes) || bytes <= 0) {
    return '0 B'
  }

  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  // Cap index at the last unit so inputs > 1 PiB don't blow past the array.
  const i = Math.min(
    Math.floor(Math.log(bytes) / Math.log(k)),
    sizes.length - 1,
  )

  return `${(bytes / Math.pow(k, i)).toFixed(2)} ${sizes[i]}`
}