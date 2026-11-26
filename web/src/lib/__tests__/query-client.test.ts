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
import { CancelledError, type QueryClient } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders, CanceledError } from 'axios'
import { toast } from 'sonner'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { createAppQueryClient } from '../query-client'
import {
  createServerError,
  isRetryableQueryError,
  queryRetryDelay,
} from '../server-error-message'
import { statusQueryOptions } from '../status-query'

function httpError(status: number, headers: Record<string, string> = {}) {
  return new AxiosError(
    `Request failed with status code ${status}`,
    status >= 500 ? 'ERR_BAD_RESPONSE' : 'ERR_BAD_REQUEST',
    undefined,
    undefined,
    {
      data: '',
      status,
      statusText: '',
      headers: AxiosHeaders.from(headers),
      config: { headers: new AxiosHeaders() },
    }
  )
}

const networkError = new AxiosError('Network Error', 'ERR_NETWORK')
const poolExhausted = createServerError(
  { success: false, message: 'Failed to query logs' },
  'Failed to load logs'
)

/** How many retries the policy grants an error before giving up. */
function retriesFor(error: unknown): number {
  let failures = 0
  while (failures < 10 && isRetryableQueryError(error, failures)) failures++
  return failures
}

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('query retry policy', () => {
  test.each([
    ['a network error', networkError, 3],
    ['a 500', httpError(500), 3],
    ['a 502 from the proxy', httpError(502), 3],
    ['a 503', httpError(503), 3],
    ['a 429 asking for 5 s', httpError(429, { 'Retry-After': '5' }), 2],
    ['a 429 asking for 10 s', httpError(429, { 'Retry-After': '10' }), 2],
    ['a 429 asking for 30 s', httpError(429, { 'Retry-After': '30' }), 0],
    ['a 429 without Retry-After', httpError(429), 0],
    ['a success:false payload (HTTP 200)', poolExhausted, 1],
    ['a 401', httpError(401), 0],
    ['a 403', httpError(403), 0],
    ['a 404', httpError(404), 0],
    ['a 400', httpError(400), 0],
    ['an axios cancellation', new CanceledError(), 0],
    ['a query cancellation', new CancelledError(), 0],
  ])('%s gets %d retries', (_name, error, retries) => {
    expect(retriesFor(error)).toBe(retries)
  })

  test('waits by Retry-After when the server sends one, else backs off to 8 s', () => {
    expect(
      [0, 1, 2, 3, 4].map((n) => queryRetryDelay(n, networkError))
    ).toEqual([1000, 2000, 4000, 8000, 8000])
    expect(queryRetryDelay(0, httpError(429, { 'Retry-After': '3' }))).toBe(
      3000
    )
    expect(queryRetryDelay(2, httpError(503, { 'Retry-After': '120' }))).toBe(
      10_000
    )
  })

  test('the app client uses the policy outside development', () => {
    vi.stubEnv('DEV', false)
    const retry = createAppQueryClient().getDefaultOptions().queries?.retry
    if (typeof retry !== 'function') throw new Error('retry is not a policy')

    expect(retry(0, httpError(503))).toBe(true)
    expect(retry(3, httpError(503))).toBe(false)
    expect(retry(0, httpError(403))).toBe(false)
    expect(createAppQueryClient().getDefaultOptions().queries?.retryDelay).toBe(
      queryRetryDelay
    )
  })
})

describe('the /500 page', () => {
  async function failQuery(
    client: QueryClient,
    meta?: Record<string, unknown>
  ) {
    await client
      .fetchQuery({
        queryKey: ['probe', meta],
        queryFn: () => Promise.reject(httpError(500)),
        retry: false,
        meta,
      })
      .catch(() => undefined)
  }

  test('a 500 on an ordinary query opens the error page', async () => {
    vi.spyOn(toast, 'error').mockReturnValue('toast')
    const openErrorPage = vi.fn()

    await failQuery(createAppQueryClient(openErrorPage))

    expect(openErrorPage).toHaveBeenCalledTimes(1)
  })

  test('a 500 on an optional query fails in place', async () => {
    vi.spyOn(toast, 'error').mockReturnValue('toast')
    const openErrorPage = vi.fn()

    await failQuery(createAppQueryClient(openErrorPage), { errorPage: false })

    expect(openErrorPage).not.toHaveBeenCalled()
    expect(toast.error).toHaveBeenCalledTimes(1)
  })

  test('a failed /api/status never replaces the page', () => {
    expect(statusQueryOptions.meta).toEqual({ errorPage: false })
  })
})
