/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Detection and one-shot recovery for lazy route chunks that fail to load.
 *
 * Production builds load route code through rspack JSONP chunks. When a
 * deployment replaces the hashed chunk files while a tab still runs the old
 * bundle, the next navigation throws a `ChunkLoadError` ("Loading chunk N
 * failed."). TanStack Router caches that rejection for the lifetime of the
 * document, so the only way back to a working page is a full reload.
 */

const CHUNK_LOAD_MESSAGE_PREFIXES = [
  'Loading chunk',
  'Loading CSS chunk',
  'Failed to fetch dynamically imported module',
  'error loading dynamically imported module',
  'Importing a module script failed',
] as const

const RELOAD_STORAGE_KEY_PREFIX = 'newapi:chunk-reload:'

export function isChunkLoadError(error: unknown): boolean {
  if (typeof error !== 'object' || error === null) return false
  const candidate = error as Record<string, unknown>
  if (candidate.name === 'ChunkLoadError') return true
  const message = candidate.message
  if (typeof message !== 'string') return false
  return CHUNK_LOAD_MESSAGE_PREFIXES.some((prefix) =>
    message.startsWith(prefix)
  )
}

/**
 * Reload the document once per failed chunk for the lifetime of the tab.
 * The identity is the content-hashed chunk URL, so a genuine redeploy (new
 * URL) still gets its reload, while a permanently missing or stalled asset
 * can never turn into a reload loop no matter how long the failure takes to
 * surface (rspack reports a hung script only after 120 s). Storage errors
 * (private mode, disabled site data) also return `false` for the same reason.
 */
export function reloadOnceForChunkError(error: unknown): boolean {
  if (!isChunkLoadError(error) || typeof window === 'undefined') return false

  const failed = error as Record<string, unknown>
  const request = failed.request
  const identity =
    typeof request === 'string' && request
      ? request
      : String(failed.message ?? '')
  const key = RELOAD_STORAGE_KEY_PREFIX + identity

  try {
    if (window.sessionStorage.getItem(key)) return false
    window.sessionStorage.setItem(key, '1')
  } catch {
    return false
  }

  window.location.reload()
  return true
}
