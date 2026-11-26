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
import { QueryClient } from '@tanstack/react-query'
import { isRedirect } from '@tanstack/react-router'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import type { SetupResponse } from '@/features/setup/types'
import { useAuthStore } from '@/stores/auth-store'

const mocks = vi.hoisted(() => ({
  getStatus: vi.fn<() => Promise<Record<string, unknown>>>(),
  getSetupStatus: vi.fn<(config?: unknown) => Promise<SetupResponse>>(),
}))

vi.mock('@/lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api')>()),
  getStatus: mocks.getStatus,
}))

vi.mock('@/features/setup/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/setup/api')>()),
  getSetupStatus: mocks.getSetupStatus,
}))

type RootBeforeLoad = (context: {
  context: { queryClient: QueryClient }
  location: { href: string; pathname: string }
}) => Promise<void>

let queryClient: QueryClient
let beforeLoad: RootBeforeLoad

/** One hard load: a fresh module (its "already checked" flag) and cache. */
async function loadApp(options: { statusRetries?: number } = {}) {
  vi.resetModules()
  const { Route } = await import('@/routes/__root')
  beforeLoad = Route.options.beforeLoad as unknown as RootBeforeLoad
  queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: options.statusRetries ?? false,
        retryDelay: 60_000,
      },
    },
  })
}

async function navigate(pathname = '/pricing') {
  try {
    await beforeLoad({
      context: { queryClient },
      location: { href: pathname, pathname },
    })
  } catch (error) {
    return error
  }
  return undefined
}

function setupResponse(initialised: boolean): SetupResponse {
  return {
    success: true,
    data: {
      status: initialised,
      root_init: initialised,
      database_type: 'sqlite',
    },
  } as SetupResponse
}

beforeEach(() => {
  useAuthStore.getState().auth.reset('idle')
})

afterEach(async () => {
  await queryClient?.cancelQueries()
  queryClient?.clear()
  mocks.getStatus.mockReset()
  mocks.getSetupStatus.mockReset()
})

describe('root setup guard', () => {
  test('an initialised system is read from /api/status without /api/setup', async () => {
    await loadApp()
    mocks.getStatus.mockResolvedValue({ setup: true, system_name: 'New API' })

    expect(await navigate()).toBeUndefined()
    expect(await navigate('/rankings')).toBeUndefined()

    expect(mocks.getSetupStatus).not.toHaveBeenCalled()
    expect(mocks.getStatus).toHaveBeenCalledTimes(1)
  })

  test('an uninitialised system, confirmed by /api/setup, goes to /setup', async () => {
    await loadApp()
    mocks.getStatus.mockResolvedValue({ setup: false })
    mocks.getSetupStatus.mockResolvedValue(setupResponse(false))

    const thrown = await navigate()

    expect(isRedirect(thrown)).toBe(true)
    expect((thrown as { options: unknown }).options).toMatchObject({
      to: '/setup',
    })
    expect(mocks.getSetupStatus).toHaveBeenCalledWith({ timeout: 8000 })
  })

  test('a stale "not initialised" snapshot does not send a finished setup back to /setup', async () => {
    await loadApp()
    mocks.getStatus.mockResolvedValue({ setup: false })
    mocks.getSetupStatus.mockResolvedValue(setupResponse(true))

    expect(await navigate('/')).toBeUndefined()
  })

  test('when /api/status fails, /api/setup decides with an 8 s timeout', async () => {
    await loadApp()
    mocks.getStatus.mockRejectedValue(
      new Error('Request failed with status code 502')
    )
    mocks.getSetupStatus.mockResolvedValue(setupResponse(false))

    const thrown = await navigate()

    expect(mocks.getSetupStatus).toHaveBeenCalledWith({ timeout: 8000 })
    expect(isRedirect(thrown)).toBe(true)
  })

  test('when both fail the page still opens, and the next navigation checks again', async () => {
    await loadApp()
    mocks.getStatus.mockRejectedValue(new Error('offline'))
    mocks.getSetupStatus.mockRejectedValue(
      new Error('timeout of 8000ms exceeded')
    )

    expect(await navigate()).toBeUndefined()

    mocks.getStatus.mockResolvedValue({ setup: true })
    expect(await navigate('/rankings')).toBeUndefined()
    expect(mocks.getStatus).toHaveBeenCalledTimes(2)
  })

  test('a failing /api/status is not waited for through its retries', async () => {
    await loadApp({ statusRetries: 3 })
    mocks.getStatus.mockRejectedValue(
      new Error('Request failed with status code 500')
    )
    mocks.getSetupStatus.mockResolvedValue(setupResponse(true))

    // retryDelay is 60 s: the guard must answer after the first failure.
    expect(await navigate()).toBeUndefined()
    expect(mocks.getStatus).toHaveBeenCalledTimes(1)
    expect(mocks.getSetupStatus).toHaveBeenCalledTimes(1)
  })

  test('a hung /api/status is given up after 8 s for /api/setup', async () => {
    await loadApp()
    vi.useFakeTimers()
    try {
      mocks.getStatus.mockReturnValue(new Promise(() => {}))
      mocks.getSetupStatus.mockResolvedValue(setupResponse(true))

      let done = false
      const guard = navigate().then((thrown) => {
        done = true
        return thrown
      })
      await vi.advanceTimersByTimeAsync(7_999)
      expect(done).toBe(false)
      expect(mocks.getSetupStatus).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(1)
      expect(await guard).toBeUndefined()
      expect(mocks.getSetupStatus).toHaveBeenCalledWith({ timeout: 8000 })
    } finally {
      vi.useRealTimers()
    }
  })

  test('the setup page itself is not checked', async () => {
    await loadApp()

    expect(await navigate('/setup')).toBeUndefined()

    expect(mocks.getStatus).not.toHaveBeenCalled()
    expect(mocks.getSetupStatus).not.toHaveBeenCalled()
  })
})
