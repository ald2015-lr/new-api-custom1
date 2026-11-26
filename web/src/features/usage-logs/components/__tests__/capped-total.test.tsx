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
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { PageFooterProvider } from '@/components/layout/components/page-footer'
import { api } from '@/lib/api'
import { resolveAuthentication } from '@/lib/auth-session'
import { ROLE } from '@/lib/roles'
import { Route as UsageLogsRoute } from '@/routes/_authenticated/usage-logs/$section'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { usageLogSchema } from '../../data/schema'
import { UsageLogs } from '../../index'
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
  /** Logs matching a search by model name, in either view. */
  modelMatches?: number
  /** While set, log list answers wait here until released. */
  held?: Array<() => void>
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
      let matches = ownList ? server.selfMatches : server.allMatches
      if (params.has('model_name')) matches = server.modelMatches ?? 0
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
      const held = server.held
      if (held) await new Promise<void>((resolve) => held.push(resolve))
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
function renderLogs(props: {
  server: Server
  search: string
  role?: number
  page?: typeof LogsTablePage
}) {
  // Signed in: the session is in memory.
  useAuthStore.getState().auth.setBundle({
    access_token: 'test-access-token',
    token_type: 'Bearer',
    access_expires_at: Math.floor(Date.now() / 1000) + 3600,
    user: { id: 1, username: 'tester', role: props.role ?? ROLE.USER },
    session: {
      sid: 'test-session',
      current: true,
      login_method: 'password',
      ip: '',
      user_agent: '',
      created_at: 0,
      last_active_at: 0,
      expires_at: 0,
    },
  })
  mockServer(props.server)
  const root = createRootRoute()
  // Like the console's route guard, check the session on every navigation.
  // With the session in memory that only writes the auth store, which the
  // log page reads (its sidebar config), so the page renders while each
  // navigation is pending. After 50 navigations the check stops: a page that
  // keeps navigating then fails its test instead of freezing it.
  let sessionChecks = 0
  const auth = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
    beforeLoad: async () => {
      sessionChecks += 1
      if (sessionChecks <= 50) await resolveAuthentication()
    },
  })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: props.page ?? LogsTablePage,
    validateSearch: UsageLogsRoute.options.validateSearch,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [`/usage-logs/common?${props.search}`],
    }),
  })
  vi.spyOn(router, 'navigate')
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
      const search = params.has('model_name') ? ' by model' : ''
      return `${view}:${params.get('p')}/${params.get('page_size')}${search}`
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

it('keeps an exact total below the cap and sends a page past its end back to the first once', async () => {
  // 25 logs are 3 pages of 10.
  const router = renderLogs({
    server: { allMatches: 0, selfMatches: 25 },
    search: 'page=5&pageSize=10',
    page: UsageLogs,
  })

  await waitFor(() => expect(requestedPages()).toEqual([5, 1]))
  expect(await screen.findByText('25')).toBeVisible()
  expect(screen.queryByText(/^\d[\d,.\s\u00a0\u202f]*\+$/)).toBeNull()
  expect(router.state.location.search).not.toHaveProperty('page')
  expect(router.navigate).toHaveBeenCalledOnce()
})

it('sends a saved link past the end of a capped list back to the first page once', async () => {
  // 12,000 logs are 1,200 pages of 10.
  const router = renderLogs({
    server: { allMatches: 0, selfMatches: 12000 },
    search: 'page=1500&pageSize=10',
    page: UsageLogs,
  })

  await waitFor(() => expect(requestedPages()).toEqual([1500, 1]))
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('page')
  )
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  expect(nextPageButton()).toBeEnabled()
  expect(screen.queryByText('No Logs Found')).toBeNull()
  expect(router.navigate).toHaveBeenCalledOnce()
})

it('sends Only Mine back to the first page when the open page lies past its end', async () => {
  const user = userEvent.setup()
  // Page 2,000 of every log exists; 15,000 own logs are 1,500 pages of 10.
  const router = renderLogs({
    server: { allMatches: 1000000, selfMatches: 15000 },
    search: 'page=2000&pageSize=10',
    role: ROLE.SUPER_ADMIN,
    page: UsageLogs,
  })
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  expect(listRequests()).toEqual(['all:2000/10'])

  await user.click(screen.getByRole('tab', { name: 'Only Mine' }))

  await waitFor(() =>
    expect(listRequests()).toEqual(['all:2000/10', 'self:2000/10', 'self:1/10'])
  )
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('page')
  )
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  expect(screen.queryByText('No Logs Found')).toBeNull()
  expect(router.navigate).toHaveBeenCalledOnce()
})

it('sends Only Mine back to the first page once when its exact total ends before the open page', async () => {
  const user = userEvent.setup()
  // Page 2,000 of every log exists; 25 own logs are 3 pages of 10.
  const router = renderLogs({
    server: { allMatches: 1000000, selfMatches: 25 },
    search: 'page=2000&pageSize=10',
    role: ROLE.SUPER_ADMIN,
    page: UsageLogs,
  })
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()

  await user.click(screen.getByRole('tab', { name: 'Only Mine' }))

  await waitFor(() =>
    expect(listRequests()).toEqual(['all:2000/10', 'self:2000/10', 'self:1/10'])
  )
  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('page')
  )
  expect(await screen.findByText('25')).toBeVisible()
  expect(screen.queryByText('No Logs Found')).toBeNull()
  expect(router.navigate).toHaveBeenCalledOnce()
})

it('goes back to the last full page of a capped list when the next page comes back empty', async () => {
  const user = userEvent.setup()
  // 10,050 logs fill exactly 1,005 pages of 10: page 1,006 is empty.
  const router = renderLogs({
    server: { allMatches: 0, selfMatches: 10050 },
    search: 'page=1005&pageSize=10',
  })
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  await waitFor(() => expect(nextPageButton()).toBeEnabled())

  await user.click(nextPageButton())

  await waitFor(() => expect(requestedPages()).toContain(1006))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ page: 1005 })
  )
  expect(nextPageButton()).toBeDisabled()
  expect(screen.getByText('1005', { selector: 'button' })).toBeVisible()
  expect(screen.queryByText('1006', { selector: 'button' })).toBeNull()
  expect(screen.queryByText('No Logs Found')).toBeNull()
})

it('pages on past the page that ended a capped list once the search changes', async () => {
  const user = userEvent.setup()
  // 10,050 logs end on page 1,005 of 10; a search by model matches 20,000.
  const server: Server = {
    allMatches: 0,
    selfMatches: 10050,
    modelMatches: 20000,
  }
  const router = renderLogs({ server, search: 'page=1005&pageSize=10' })
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  await waitFor(() => expect(nextPageButton()).toBeEnabled())
  await user.click(nextPageButton())
  await waitFor(() => expect(requestedPages()).toContain(1006))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ page: 1005 })
  )
  expect(nextPageButton()).toBeDisabled()

  // A new search starts on its first page; then its page 1,005 opens.
  await act(() =>
    router.navigate({
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: { pageSize: 10, model: 'gpt-4o' },
    })
  )
  await waitFor(() => expect(listRequests()).toContain('self:1/10 by model'))
  await act(() =>
    router.navigate({
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: { pageSize: 10, model: 'gpt-4o', page: 1005 },
    })
  )
  await waitFor(() => expect(listRequests()).toContain('self:1005/10 by model'))

  await waitFor(() => expect(nextPageButton()).toBeEnabled())
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

it('keeps the next page of a capped list while a larger page size loads', async () => {
  const user = userEvent.setup()
  const server: Server = { allMatches: 0, selfMatches: 20000 }
  renderLogs({ server, search: 'pageSize=20' })
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  await waitFor(() => expect(nextPageButton()).toBeEnabled())

  // The 20 rows on screen stay until the 100-row page arrives.
  server.held = []
  const rowsPerPage = screen
    .getAllByRole('combobox')
    .find((element) => element.textContent?.trim() === '20')
  expect(rowsPerPage).toBeDefined()
  await user.click(rowsPerPage as HTMLElement)
  await user.click(await screen.findByRole('option', { name: '100' }))
  await waitFor(() => expect(listRequests()).toContain('self:1/100'))

  expect(nextPageButton()).toBeEnabled()
  await act(async () => server.held?.splice(0).forEach((resolve) => resolve()))
})

it('keeps the next page of a capped list while the page before the short last page loads', async () => {
  const user = userEvent.setup()
  // 10,037 logs: page 1,004 of 10 holds the last 7.
  const server: Server = { allMatches: 0, selfMatches: 10037 }
  renderLogs({ server, search: 'page=1004&pageSize=10' })
  expect(await screen.findByText(CAPPED_TOTAL)).toBeVisible()
  await waitFor(() => expect(nextPageButton()).toBeDisabled())

  // The short last page stays on screen until page 1,003 arrives.
  server.held = []
  await user.click(screen.getByRole('button', { name: 'Go to previous page' }))
  await waitFor(() => expect(requestedPages()).toEqual([1004, 1003]))

  expect(nextPageButton()).toBeEnabled()
  await act(async () => server.held?.splice(0).forEach((resolve) => resolve()))
})
