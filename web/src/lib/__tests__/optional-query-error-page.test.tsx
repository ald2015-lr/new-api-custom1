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
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { cleanup, render, renderHook, waitFor } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi, type Mock } from 'vitest'

import { PerformanceOverview } from '@/features/dashboard/components/models/performance-overview'
import { PerformanceHealthPanel } from '@/features/dashboard/components/overview/performance-health-panel'
import {
  MODEL_PRICING_ISSUES_QUERY_KEY,
  useModelPricingIssues,
} from '@/features/system-settings/models/pricing-issues'
import { useNotifications } from '@/hooks/use-notifications'
import { api } from '@/lib/api'
import { createAppQueryClient } from '@/lib/query-client'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

// Optional queries (side panels, badges) carry meta.errorPage false, so a 500
// fails in place instead of replacing the whole page with /500. The status
// query and the model square's performance queries are covered next to their
// code; these are the others.

function internalServerError() {
  return new AxiosError(
    'Request failed with status code 500',
    'ERR_BAD_RESPONSE',
    undefined,
    undefined,
    {
      data: '',
      status: 500,
      statusText: '',
      headers: new AxiosHeaders(),
      config: { headers: new AxiosHeaders() },
    }
  )
}

let client: QueryClient
// What the app's query client calls to open the /500 page.
let openErrorPage: Mock<() => void>

beforeEach(() => {
  openErrorPage = vi.fn<() => void>()
  client = createAppQueryClient(openErrorPage)
  vi.spyOn(toast, 'error').mockReturnValue('toast')
  vi.spyOn(api, 'get').mockRejectedValue(internalServerError())
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

/** Waits until the query with this key has failed. */
async function queryFailed(queryKey: readonly unknown[]) {
  await waitFor(() =>
    expect(client.getQueryState(queryKey)?.status).toBe('error')
  )
}

test.each([
  ['the overview health panel', PerformanceHealthPanel],
  ['the models performance overview', PerformanceOverview],
])(
  '%s fails in place when the metrics summary answers 500',
  async (_, Panel) => {
    render(<Panel />, { wrapper })

    await queryFailed(['dashboard-perf-metrics-summary', 24])

    expect(openErrorPage).not.toHaveBeenCalled()
  }
)

test('the notice behind the bell fails in place when it answers 500', async () => {
  renderHook(() => useNotifications(), { wrapper })

  await queryFailed(['notice'])

  expect(openErrorPage).not.toHaveBeenCalled()
})

test('the pricing issue badge fails in place when the check answers 500', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'root', role: ROLE.SUPER_ADMIN })
  renderHook(() => useModelPricingIssues(), { wrapper })

  await queryFailed(MODEL_PRICING_ISSUES_QUERY_KEY)

  expect(openErrorPage).not.toHaveBeenCalled()
})
