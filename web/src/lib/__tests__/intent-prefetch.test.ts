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

import {
  createIntentPrefetch,
  INTENT_PREFETCH_DELAY_MS,
} from '../idle-prefetch'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('createIntentPrefetch', () => {
  test('prefetches once the pointer or focus has rested on a control', () => {
    const intent = createIntentPrefetch()
    const prefetch = vi.fn()

    intent.start(prefetch)
    vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS - 1)
    expect(prefetch).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)

    expect(prefetch).toHaveBeenCalledTimes(1)
  })

  test('a control the pointer only crosses prefetches nothing', () => {
    const intent = createIntentPrefetch()
    const crossed = vi.fn()
    const target = vi.fn()

    intent.start(crossed)
    vi.advanceTimersByTime(120)
    intent.cancel()
    intent.start(target)
    vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS * 5)

    expect(crossed).not.toHaveBeenCalled()
    expect(target).toHaveBeenCalledTimes(1)
  })

  test('a new start replaces a prefetch still waiting', () => {
    const intent = createIntentPrefetch()
    const first = vi.fn()
    const second = vi.fn()

    intent.start(first)
    vi.advanceTimersByTime(100)
    intent.start(second)
    vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS * 5)

    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
  })

  test.each([
    { connection: { saveData: true }, why: 'asks to save data' },
    { connection: { effectiveType: '2g' }, why: 'reports a 2G connection' },
  ])('a browser that $why does not prefetch on intent', ({ connection }) => {
    vi.stubGlobal(
      'navigator',
      Object.assign(Object.create(navigator), { connection })
    )
    const intent = createIntentPrefetch()
    const prefetch = vi.fn()

    intent.start(prefetch)
    vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS * 5)

    expect(prefetch).not.toHaveBeenCalled()
  })
})
