/**
 * Format bytes to human-readable string (B, KB, MB, GB)
 * 2026-07-25: Added for dashboard body size statistics display
 */
export function formatBytes(bytes?: number): string {
  if (!bytes || bytes === 0) return '0 B'
  
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  
  return `${(bytes / Math.pow(k, i)).toFixed(2)} ${sizes[i]}`
}
