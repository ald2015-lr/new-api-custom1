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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { PageFooterProvider } from '@/components/layout/components/page-footer'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { Route as UsageLogsRoute } from '@/routes/_authenticated/usage-logs/$section'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { usageLogSchema } from '../../data/schema'
import { UsageLogsProvider } from '../usage-logs-provider'
import { UsageLogsTable } from '../usage-logs-table'

const PAGE_SIZE = 10
// The server's count cap; past it the list answers total_capped.
const COUNT_CAP = 10000
// "10,000+" in whatever digit grouping the test runtime formats with.
const CAPPED_TOTAL = /^10[,.\s\u00a0\u202f]?000\+$/

function LogsPage() {
  const [footer, setFooter] = useState<HTMLDivElement | null>(null)
  return (
    <PageFooterProvider container={footer}>
      <UsageLogsProvider>
        <UsageLogsTable logCategory='common' />
      </UsageLogsProvider>
      <div ref={setFooter} />
    </PageFooterProvider>
  )
}

/**
 * Renders a user's own log list at page over `matches` logs, answered like
 * the server: a page of rows, the count up to COUNT_CAP and total_capped.
 */
function renderLogs(props: { matches: number; page: number }) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'tester',
    role: ROLE.USER,
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = { quota: 0, rpm: 0, tpm: 0 }
    if (url.startsWith('/api/log/self?')) {
      const page = Number(new URLSearchParams(url.split('?')[1]).get('p'))
      const rows = Math.max(
        0,
        Math.min(PAGE_SIZE, props.matches - (page - 1) * PAGE_SIZE)
      )
      data = {
        items: Array.from({ length: rows }, (_, index) =>
          usageLogSchema.parse({
            id: (page - 1) * PAGE_SIZE + index + 1,
            user_id: 1,
            created_at: 1788840000,
            type: 2,
            content: '',
            quota: 5000,
          })
        ),
        total: Math.min(props.matches, COUNT_CAP),
        total_capped: props.matches > COUNT_CAP,
        page,
        page_size: PAGE_SIZE,
      }
    } else if (url === '/api/subscription/self') {
      data = { subscriptions: [] }
    } else if (url === '/api/user/self/groups') {
      data = {}
    }
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: LogsPage,
    validateSearch: UsageLogsRoute.options.validateSearch,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [
        `/usage-logs/common?page=${props.page}&pageSize=${PAGE_SIZE}`,
      ],
    }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return router
}

/** The pages of the log list that were requested, in order. */
function requestedPages() {
  return vi
    .mocked(api.get)
    .mock.calls.map(([url]) => url)
    .filter((url) => url.startsWith('/api/log/self?'))
    .map((url) => Number(new URLSearchParams(url.split('?')[1]).get('p')))
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
})

it('shows a capped total as 10,000+ and pages on past the counted pages', async () => {
  const user = userEvent.setup()
  const router = renderLogs({ matches: 20000, page: 1001 })

  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  const next = screen.getByRole('button', { name: 'Go to next page' })
  expect(next).toBeEnabled()
  await user.click(next)

  await waitFor(() => expect(requestedPages()).toContain(1002))
  expect(router.state.location.search).toMatchObject({ page: 1002 })
  expect(requestedPages()).not.toContain(1)
})

it('ends a capped list at the first short page', async () => {
  renderLogs({ matches: COUNT_CAP + 5, page: 1001 })

  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeDisabled()
  )
  expect(screen.getByText('1001', { selector: 'button' })).toBeVisible()
})

it('keeps an exact total below the cap and sends a page past its end back to the first', async () => {
  const router = renderLogs({ matches: 25, page: 5 })

  await waitFor(() => expect(requestedPages()).toContain(1))
  expect(await screen.findByText('25')).toBeVisible()
  expect(screen.queryByText(/^\d[\d,.\s\u00a0\u202f]*\+$/)).toBeNull()
  expect(router.state.location.search).not.toHaveProperty('page')
})
