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

// The server's count cap; past it the list answers total_capped.
const COUNT_CAP = 10000
// The most logs the server answers per page (common.GetPageQuery).
const SERVER_MAX_PAGE_SIZE = 100
// "10,000+" in whatever digit grouping the test runtime formats with.
const CAPPED_TOTAL = /^10[,.\s\u00a0\u202f]?000\+$/

function LogsTablePage() {
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

type Server = {
  /** Logs matching the All view (/api/log/) and the own view (/api/log/self). */
  allMatches: number
  selfMatches: number
}

/**
 * Answers the log lists like the server: a page of rows (at most 100), the
 * count up to COUNT_CAP and total_capped.
 */
function mockServer(server: Server) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const ownList = url.startsWith('/api/log/self?')
    if (ownList || url.startsWith('/api/log?')) {
      const params = new URLSearchParams(url.split('?')[1])
      const page = Number(params.get('p'))
      const size = Math.min(
        SERVER_MAX_PAGE_SIZE,
        Number(params.get('page_size'))
      )
      const matches = ownList ? server.selfMatches : server.allMatches
      const rows = Math.max(0, Math.min(size, matches - (page - 1) * size))
      const data = {
        items: Array.from({ length: rows }, (_, index) =>
          usageLogSchema.parse({
            id: (page - 1) * size + index + 1,
            user_id: 1,
            created_at: 1788840000,
            type: 2,
            content: '',
            quota: 5000,
          })
        ),
        total: Math.min(matches, COUNT_CAP),
        total_capped: matches > COUNT_CAP,
        page,
        page_size: size,
      }
      return { data: { success: true, data } }
    }
    let data: unknown = { quota: 0, rpm: 0, tpm: 0 }
    if (url === '/api/subscription/self') {
      data = { subscriptions: [] }
    } else if (url.startsWith('/api/subscription/') || url === '/api/group/') {
      data = []
    } else if (url === '/api/user/self/groups') {
      data = {}
    }
    return { data: { success: true, data } }
  })
}

/** Renders the common log list at the given URL search, answered by server. */
function renderLogs(props: { server: Server; search: string }) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'tester',
    role: ROLE.USER,
  })
  mockServer(props.server)
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: LogsTablePage,
    validateSearch: UsageLogsRoute.options.validateSearch,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [`/usage-logs/common?${props.search}`],
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

/** The log list requests made, in order, as view:page/page size. */
function listRequests() {
  return vi
    .mocked(api.get)
    .mock.calls.map(([url]) => url)
    .filter((url) => /^\/api\/log(\/self)?\?/.test(url))
    .map((url) => {
      const params = new URLSearchParams(url.split('?')[1])
      const view = url.startsWith('/api/log/self?') ? 'self' : 'all'
      return `${view}:${params.get('p')}/${params.get('page_size')}`
    })
}

/** The pages of the own log list that were requested, in order. */
function requestedPages() {
  return listRequests()
    .filter((request) => request.startsWith('self:'))
    .map((request) => Number(request.slice('self:'.length).split('/')[0]))
}

function nextPageButton() {
  return screen.getByRole('button', { name: 'Go to next page' })
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
  const router = renderLogs({
    server: { allMatches: 0, selfMatches: 20000 },
    search: 'page=1001&pageSize=10',
  })

  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  const next = nextPageButton()
  expect(next).toBeEnabled()
  await user.click(next)

  await waitFor(() => expect(requestedPages()).toContain(1002))
  expect(router.state.location.search).toMatchObject({ page: 1002 })
  expect(requestedPages()).not.toContain(1)
})

it('ends a capped list at the first short page', async () => {
  renderLogs({
    server: { allMatches: 0, selfMatches: COUNT_CAP + 5 },
    search: 'page=1001&pageSize=10',
  })

  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  await waitFor(() => expect(nextPageButton()).toBeDisabled())
  expect(screen.getByText('1001', { selector: 'button' })).toBeVisible()
})

it('keeps an exact total below the cap and sends a page past its end back to the first', async () => {
  const router = renderLogs({
    server: { allMatches: 0, selfMatches: 25 },
    search: 'page=5&pageSize=10',
  })

  await waitFor(() => expect(requestedPages()).toContain(1))
  expect(await screen.findByText('25')).toBeVisible()
  expect(screen.queryByText(/^\d[\d,.\s\u00a0\u202f]*\+$/)).toBeNull()
  expect(router.state.location.search).not.toHaveProperty('page')
})

it('reads a page size above the server maximum as the maximum and keeps paging', async () => {
  const router = renderLogs({
    server: { allMatches: 0, selfMatches: 20000 },
    search: 'pageSize=200',
  })

  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  expect(listRequests()).toEqual(['self:1/100'])
  expect(router.state.location.search).toMatchObject({ pageSize: 100 })
  await waitFor(() => expect(nextPageButton()).toBeEnabled())
})
