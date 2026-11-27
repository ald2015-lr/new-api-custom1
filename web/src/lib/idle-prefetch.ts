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

/** Whether the browser asks to save data or reports a 2G connection. */
export function prefersLessData(): boolean {
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

/**
 * How long the pointer or the keyboard focus has to rest on a control before
 * an intent prefetch starts. Long enough to skip a control the pointer only
 * crosses on its way to another one (about 100-150 ms for a tab).
 */
export const INTENT_PREFETCH_DELAY_MS = 200

export type IntentPrefetch = {
  /** Prefetch after INTENT_PREFETCH_DELAY_MS, replacing one still waiting. */
  start: (prefetch: () => void) => void
  /** Drop a prefetch that is still waiting (pointer left, focus moved on). */
  cancel: () => void
}

/**
 * Prefetch on intent: when the pointer or focus rests on a control, not when
 * it passes over it. Skipped when the browser asks to save data or reports a
 * 2G connection; opening the control still loads what it needs.
 */
export function createIntentPrefetch(): IntentPrefetch {
  let timer: ReturnType<typeof setTimeout> | undefined
  const cancel = () => {
    if (timer === undefined) return
    clearTimeout(timer)
    timer = undefined
  }
  return {
    start: (prefetch) => {
      cancel()
      if (prefersLessData()) return
      timer = setTimeout(() => {
        timer = undefined
        prefetch()
      }, INTENT_PREFETCH_DELAY_MS)
    },
    cancel,
  }
}
