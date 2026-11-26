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
type IdleWindow = Window & {
  requestIdleCallback?: (
    callback: () => void,
    options?: { timeout: number }
  ) => number
  cancelIdleCallback?: (handle: number) => void
}

type NetworkInformationLike = { saveData?: boolean; effectiveType?: string }

function prefersLessData(): boolean {
  const connection = (
    navigator as Navigator & { connection?: NetworkInformationLike }
  ).connection
  return (
    connection?.saveData === true ||
    connection?.effectiveType === 'slow-2g' ||
    connection?.effectiveType === '2g'
  )
}

/** Delay before an idle prefetch, so the page's own requests go first. */
export const IDLE_PREFETCH_DELAY_MS = 2000

/**
 * Run a prefetch once the page has settled: after IDLE_PREFETCH_DELAY_MS, at
 * the browser's next idle period. Skipped when the browser asks to save data
 * or reports a 2G connection. Returns a function that cancels the prefetch if
 * it has not started yet.
 */
export function prefetchWhenIdle(prefetch: () => void): () => void {
  if (typeof window === 'undefined' || prefersLessData()) return () => undefined

  const idleWindow = window as IdleWindow
  let idleHandle: number | undefined
  const timer = window.setTimeout(() => {
    if (idleWindow.requestIdleCallback) {
      idleHandle = idleWindow.requestIdleCallback(prefetch, { timeout: 5000 })
    } else {
      prefetch()
    }
  }, IDLE_PREFETCH_DELAY_MS)

  return () => {
    window.clearTimeout(timer)
    if (idleHandle !== undefined) idleWindow.cancelIdleCallback?.(idleHandle)
  }
}
