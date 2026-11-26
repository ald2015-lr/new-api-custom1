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
 * The longest the first render waits for the visitor's locale bundle. A slower
 * bundle still switches the language when it arrives (`ensureLocale`).
 */
export const LOCALE_WAIT_MS = 10_000

/**
 * Resolve when `task` settles or after `timeoutMs`, whichever comes first.
 * Never rejects. Built on `setTimeout` alone: `AbortSignal.timeout` and
 * `AbortSignal.any` are missing in older in-app browsers.
 */
export function waitAtMost(
  task: Promise<unknown>,
  timeoutMs: number
): Promise<void> {
  return new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, timeoutMs)
    const settle = () => {
      clearTimeout(timer)
      resolve()
    }
    task.then(settle).catch(settle)
  })
}
