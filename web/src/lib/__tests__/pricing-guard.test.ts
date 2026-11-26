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
import { AxiosError, AxiosHeaders } from 'axios'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import {
  PAGE_GUARD_STATUS_WAIT_MS,
  getModuleAccessForGuard,
  getModuleAccessForPageGuard,
} from '@/lib/nav-modules'
import { STATUS_QUERY_KEY, STATUS_STORAGE_KEY } from '@/lib/status-query'
import { Route as ModelDetailsRoute } from '@/routes/pricing/$modelId/index'
import { Route as PricingRoute } from '@/routes/pricing/index'
import { useAuthStore } from '@/stores/auth-store'

type Guard = (context: {
  context: { queryClient: QueryClient }
  location: { href: string }
}) => Promise<void>

const guards: Array<[string, Guard]> = [
  ['/pricing', PricingRoute.options.beforeLoad as unknown as Guard],
  [
    '/pricing/example-model',
    ModelDetailsRoute.options.beforeLoad as unknown as Guard,
  ],
]

const originalAuth = useAuthStore.getState().auth
let queryClient: QueryClient

/** A client that retries like the app's: here every retry waits a minute. */
function retryingClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: 3, retryDelay: 60_000 } },
  })
}

function statusError(status: number) {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('Request failed', 'ERR_BAD_RESPONSE', config, null, {
    data: '',
    status,
    statusText: '',
    headers: new AxiosHeaders(),
    config,
  })
}

/** /api/status answers this (or fails with it), counting the requests. */
function stubStatus(answer: () => Promise<unknown>) {
  return vi.spyOn(api, 'get').mockImplementation(async (url: string) => {
    if (url !== '/api/status') throw new Error(`unexpected GET ${url}`)
    return { data: { success: true, data: await answer() } }
  })
}

function saveSnapshot(modules: Record<string, unknown>) {
  window.localStorage.setItem(
    STATUS_STORAGE_KEY,
    JSON.stringify({ HeaderNavModules: JSON.stringify(modules) })
  )
}

/** Run a guard and return the redirect target it chose, or `null`. */
async function redirectOf(guard: Guard, href: string) {
  try {
    await guard({ context: { queryClient }, location: { href } })
    return null
  } catch (error) {
    if (!isRedirect(error)) throw error
    return error.options.to ?? null
  }
}

beforeEach(() => {
  window.localStorage.clear()
  queryClient = retryingClient()
})

afterEach(() => {
  vi.useRealTimers()
  queryClient.clear()
  window.localStorage.clear()
  useAuthStore.setState({ auth: originalAuth })
  vi.restoreAllMocks()
})

describe.each(guards)('the %s guard', (href, guard) => {
  test('status 500 on a first visit (no snapshot) opens the page with the module defaults', async () => {
    stubStatus(() => Promise.reject(statusError(500)))

    expect(await redirectOf(guard, href)).toBeNull()
  })

  test('status 500 with a snapshot that switches the model square off sends the visitor home', async () => {
    saveSnapshot({ pricing: { enabled: false, requireAuth: false } })
    stubStatus(() => Promise.reject(statusError(500)))

    expect(await redirectOf(guard, href)).toBe('/')
  })

  test('status 500 with a snapshot that requires sign-in sends an anonymous visitor to sign-in', async () => {
    saveSnapshot({ pricing: { enabled: true, requireAuth: true } })
    stubStatus(() => Promise.reject(statusError(502)))

    expect(await redirectOf(guard, href)).toBe('/sign-in')
  })

  test('a hung status request is decided from the snapshot after the wait', async () => {
    vi.useFakeTimers()
    saveSnapshot({ pricing: { enabled: false, requireAuth: false } })
    stubStatus(() => new Promise(() => undefined))

    let target: string | null | undefined
    void redirectOf(guard, href).then((result) => {
      target = result
    })
    await vi.advanceTimersByTimeAsync(PAGE_GUARD_STATUS_WAIT_MS - 1)
    expect(target).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1)
    expect(target).toBe('/')
  })

  test('a status answer within the wait wins over the snapshot', async () => {
    vi.useFakeTimers()
    saveSnapshot({ pricing: { enabled: false, requireAuth: false } })
    stubStatus(
      () =>
        new Promise((resolve) =>
          setTimeout(
            () =>
              resolve({
                HeaderNavModules: { pricing: { enabled: true } },
              }),
            1000
          )
        )
    )

    let target: string | null | undefined
    void redirectOf(guard, href).then((result) => {
      target = result
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(target).toBeNull()
  })
})

describe('getModuleAccessForPageGuard', () => {
  test('a fresh cached status decides at once, without a request', async () => {
    queryClient.setQueryData(STATUS_QUERY_KEY, {
      HeaderNavModules: { pricing: { enabled: true, requireAuth: true } },
    })
    const get = stubStatus(() => Promise.resolve({}))

    expect(await getModuleAccessForPageGuard(queryClient, 'pricing')).toEqual({
      enabled: true,
      requireAuth: true,
    })
    expect(get).not.toHaveBeenCalled()
  })

  test('a stale cached status is refreshed first, and kept when the refresh fails', async () => {
    queryClient.setQueryData(
      STATUS_QUERY_KEY,
      { HeaderNavModules: { pricing: false } },
      { updatedAt: Date.now() - 600_000 }
    )
    stubStatus(() =>
      Promise.resolve({ HeaderNavModules: { pricing: { enabled: true } } })
    )
    expect(await getModuleAccessForPageGuard(queryClient, 'pricing')).toEqual({
      enabled: true,
      requireAuth: false,
    })

    queryClient.setQueryData(
      STATUS_QUERY_KEY,
      { HeaderNavModules: { pricing: false } },
      { updatedAt: Date.now() - 600_000 }
    )
    vi.restoreAllMocks()
    stubStatus(() => Promise.reject(statusError(500)))
    expect(await getModuleAccessForPageGuard(queryClient, 'pricing')).toEqual({
      enabled: false,
      requireAuth: false,
    })
  })

  test('the rankings guard keeps failing closed', async () => {
    stubStatus(() => Promise.reject(statusError(500)))

    expect(
      await getModuleAccessForGuard(
        new QueryClient({ defaultOptions: { queries: { retry: false } } }),
        'rankings'
      )
    ).toEqual({ enabled: false, requireAuth: true })
  })
})
