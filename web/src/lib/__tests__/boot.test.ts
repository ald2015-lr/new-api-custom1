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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { LOCALE_WAIT_MS, waitAtMost } from '../boot'

describe('first render after the locale bundle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  test('renders after 10 s when the locale bundle hangs', async () => {
    const render = vi.fn()
    void waitAtMost(new Promise(() => {}), LOCALE_WAIT_MS).then(render)

    await vi.advanceTimersByTimeAsync(9_999)
    expect(render).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)
    expect(render).toHaveBeenCalledTimes(1)
    expect(LOCALE_WAIT_MS).toBe(10_000)
  })

  test('renders at once when the locale bundle arrives', async () => {
    const render = vi.fn()
    let arrive = () => {}
    const locale = new Promise<void>((resolve) => {
      arrive = resolve
    })
    void waitAtMost(locale, LOCALE_WAIT_MS).then(render)

    await vi.advanceTimersByTimeAsync(300)
    expect(render).not.toHaveBeenCalled()
    arrive()
    await vi.advanceTimersByTimeAsync(0)

    expect(render).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  test('renders when the task fails, and only once', async () => {
    const render = vi.fn()
    void waitAtMost(Promise.reject(new Error('chunk failed')), 1_000).then(
      render
    )

    await vi.advanceTimersByTimeAsync(5_000)

    expect(render).toHaveBeenCalledTimes(1)
  })
})
