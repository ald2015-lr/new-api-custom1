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
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { useWaitDeadline } from '../use-wait-deadline'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

it('shows the real wait after the clock jumps while the tab was hidden', () => {
  const { result } = renderHook(() => useWaitDeadline())
  act(() => result.current.start(900))
  expect(result.current.secondsLeft).toBe(900)

  // A sleeping tab fires no interval ticks; only the clock moves on.
  act(() => {
    vi.setSystemTime(Date.now() + 10 * 60 * 1000)
    document.dispatchEvent(new Event('visibilitychange'))
  })

  expect(result.current.secondsLeft).toBe(300)
  expect(result.current.active).toBe(true)
})

it('counts down every second and ends at the deadline', () => {
  const { result } = renderHook(() => useWaitDeadline())
  act(() => result.current.start(3))

  act(() => vi.advanceTimersByTime(1000))
  expect(result.current.secondsLeft).toBe(2)

  act(() => vi.advanceTimersByTime(2000))
  expect(result.current.secondsLeft).toBe(0)
  expect(result.current.active).toBe(false)
})

it('ends a wait at once when stopped', () => {
  const { result } = renderHook(() => useWaitDeadline())
  act(() => result.current.start(60))
  act(() => result.current.stop())

  expect(result.current.active).toBe(false)
  act(() => vi.advanceTimersByTime(5000))
  expect(result.current.secondsLeft).toBe(0)
})
