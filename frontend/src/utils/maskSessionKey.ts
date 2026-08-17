/**
 * Build a stable, privacy-preserving identifier for a Session Key.
 * Normal keys reveal exactly the first four and last four characters.
 * Short values are masked more aggressively so the full secret is never shown.
 */
export function maskSessionKey(sessionKey: string): string {
  const normalized = sessionKey.trim()
  if (!normalized) return ''
  if (normalized.length <= 4) return '••••'
  if (normalized.length <= 8) return `${normalized.slice(0, 2)}…${normalized.slice(-2)}`
  return `${normalized.slice(0, 4)}…${normalized.slice(-4)}`
}
