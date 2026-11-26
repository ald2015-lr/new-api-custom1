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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { handleServerError } from '@/lib/handle-server-error'

import { getHomePageContent } from '../api'
import {
  FIRST_VISIT_CONTENT_WAIT_MS,
  useHomePageContent,
} from '../hooks/use-home-page-content'
import type { HomePageContentResponse } from '../types'

vi.mock('../api', () => ({ getHomePageContent: vi.fn() }))
vi.mock('@/lib/handle-server-error', () => ({ handleServerError: vi.fn() }))

type Reply = (response: HomePageContentResponse) => void

/** The next content request stays open until the returned reply is called. */
function holdContentRequest(): { reply: Reply; fail: (error: Error) => void } {
  let reply: Reply = () => {}
  let fail: (error: Error) => void = () => {}
  vi.mocked(getHomePageContent).mockImplementationOnce(
    () =>
      new Promise((resolve, reject) => {
        reply = resolve
        fail = reject
      })
  )
  return {
    reply: (response) => reply(response),
    fail: (error) => fail(error),
  }
}

beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
})

afterEach(() => {
  vi.useRealTimers()
  localStorage.clear()
})

describe('home page content', () => {
  test('a returning visitor renders the cached content at once and refreshes it in the background', async () => {
    localStorage.setItem('home_page_content', '# Welcome')
    localStorage.setItem('home_page_content_state', 'content')
    const request = holdContentRequest()

    const { result } = renderHook(() => useHomePageContent())

    expect(result.current).toEqual({
      content: '# Welcome',
      isLoaded: true,
      isUrl: false,
    })

    await act(async () => request.reply({ success: true, data: '# Updated' }))

    expect(result.current.content).toBe('# Updated')
    expect(localStorage.getItem('home_page_content')).toBe('# Updated')
  })

  test('content cached before the state key existed still renders at once', () => {
    localStorage.setItem('home_page_content', 'https://example.com/home')
    holdContentRequest()

    const { result } = renderHook(() => useHomePageContent())

    expect(result.current).toEqual({
      content: 'https://example.com/home',
      isLoaded: true,
      isUrl: true,
    })
  })

  test('a site known to have no custom content shows the default home at once', () => {
    localStorage.setItem('home_page_content_state', 'none')
    holdContentRequest()

    const { result } = renderHook(() => useHomePageContent())

    expect(result.current.isLoaded).toBe(true)
    expect(result.current.content).toBe('')
  })

  test('a first visit waits for the content, at most 5 s, before the default home', async () => {
    const request = holdContentRequest()

    const { result } = renderHook(() => useHomePageContent())
    expect(result.current.isLoaded).toBe(false)

    await act(() =>
      vi.advanceTimersByTimeAsync(FIRST_VISIT_CONTENT_WAIT_MS - 1)
    )
    expect(result.current.isLoaded).toBe(false)
    await act(() => vi.advanceTimersByTimeAsync(1))
    expect(result.current).toMatchObject({ content: '', isLoaded: true })
    expect(FIRST_VISIT_CONTENT_WAIT_MS).toBe(5000)

    // A late answer still replaces the default home.
    await act(async () => request.reply({ success: true, data: '<h1>Hi</h1>' }))
    expect(result.current.content).toBe('<h1>Hi</h1>')
    expect(localStorage.getItem('home_page_content_state')).toBe('content')
  })

  test('a first visit renders as soon as the content arrives', async () => {
    const request = holdContentRequest()
    const { result } = renderHook(() => useHomePageContent())

    await act(async () => request.reply({ success: true, data: '# Docs' }))

    expect(result.current).toMatchObject({ content: '# Docs', isLoaded: true })
  })

  test('an empty answer is remembered, so the next visit does not wait', async () => {
    localStorage.setItem('home_page_content', '# Old')
    const request = holdContentRequest()
    const { result } = renderHook(() => useHomePageContent())

    await act(async () => request.reply({ success: true, data: '' }))

    expect(result.current.content).toBe('')
    expect(localStorage.getItem('home_page_content')).toBe(null)
    expect(localStorage.getItem('home_page_content_state')).toBe('none')
  })

  test('a failed request keeps the cached content and reports the error', async () => {
    localStorage.setItem('home_page_content', '# Cached')
    const request = holdContentRequest()
    const { result } = renderHook(() => useHomePageContent())

    await act(async () => request.fail(new Error('Network Error')))

    expect(result.current).toMatchObject({
      content: '# Cached',
      isLoaded: true,
    })
    expect(handleServerError).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem('home_page_content')).toBe('# Cached')
  })
})
