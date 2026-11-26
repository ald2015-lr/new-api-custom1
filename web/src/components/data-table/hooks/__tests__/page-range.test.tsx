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
import {
  keepPreviousData,
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  RouterProvider,
  useNavigate,
  useSearch,
} from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  createContext,
  useContext,
  useState,
  useSyncExternalStore,
} from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import z from 'zod'

import { type NavigateFn, useTableUrlState } from '@/hooks/use-table-url-state'

import { DataTablePagination } from '../../core/pagination'
import { useDataTable } from '../use-data-table'

type Row = { id: number; name: string }

type Server = {
  /** Rows in the list. */
  matches: number
  /** Rows in the narrower view the page can switch to, like Only Mine. */
  mineMatches?: number
  /** Rows matching the name filter. */
  filteredMatches?: number
  /** Where the server stops counting: past it the total is capped. */
  cap?: number
}

// A table that navigates again whenever the page renders during a pending
// navigation restarts that navigation, so it never finishes. Past this many
// navigations the app state below stops changing and such a table fails the
// test instead of freezing it.
const LOOP_LIMIT = 50

/**
 * App state that every navigation writes while it is still pending, and that
 * the page reads. In the console the route guards (beforeLoad) resolve the
 * session on each navigation, which writes the auth store, and the usage-log
 * page reads the whole auth store through useSidebarConfig: each navigation
 * renders the page again before the router has the new search.
 */
function createAppState() {
  let writes = 0
  const listeners = new Set<() => void>()
  return {
    subscribe(listener: () => void) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    read: () => writes,
    write() {
      if (writes >= LOOP_LIMIT) return
      writes += 1
      listeners.forEach((listener) => listener())
    },
  }
}

type AppState = ReturnType<typeof createAppState>

type Harness = {
  server: Server
  appState: AppState
  /** The list requests, in order, as page/page size and the view or filter. */
  requests: string[]
  /** Pass the table a new function on each render, as inline callers do. */
  wrapEnsurePageInRange: boolean
}

const HarnessContext = createContext<Harness | null>(null)

function useHarness() {
  const harness = useContext(HarnessContext)
  if (!harness) throw new Error('rendered outside renderRows')
  return harness
}

/** Answers a page of the list like the console's list APIs. */
function answer(
  server: Server,
  request: { page: number; pageSize: number; mine: boolean; name?: string }
) {
  let matches = server.matches
  if (request.mine) matches = server.mineMatches ?? 0
  if (request.name) matches = server.filteredMatches ?? 0
  const first = (request.page - 1) * request.pageSize
  const rows = Math.max(0, Math.min(request.pageSize, matches - first))
  const cap = server.cap ?? Number.POSITIVE_INFINITY
  return {
    items: Array.from({ length: rows }, (_, index) => ({
      id: first + index + 1,
      name: `row ${first + index + 1}`,
    })),
    total: Math.min(matches, cap),
    capped: matches > cap,
  }
}

const columns: ColumnDef<Row, unknown>[] = [{ accessorKey: 'name' }]
const NO_ROWS: Row[] = []

function RowsTable(props: { mine: boolean }) {
  const harness = useHarness()
  const urlState = useTableUrlState({
    search: useSearch({ strict: false }),
    // As the console's tables pass a route's navigate. The test route is not
    // in the app's registered route tree, so the router's types reject it.
    navigate: useNavigate() as unknown as NavigateFn,
    pagination: { defaultPage: 1, defaultPageSize: 10 },
    globalFilter: { enabled: false },
    columnFilters: [{ columnId: 'name', searchKey: 'name', type: 'string' }],
  })
  const pagination = urlState.pagination
  const nameFilter = urlState.columnFilters.find(
    (filter) => filter.id === 'name'
  )?.value as string | undefined
  const query = useQuery({
    queryKey: [
      'rows',
      props.mine,
      pagination.pageIndex,
      pagination.pageSize,
      nameFilter,
    ],
    queryFn: async () => {
      const request = {
        page: pagination.pageIndex + 1,
        pageSize: pagination.pageSize,
        mine: props.mine,
        name: nameFilter,
      }
      let label = `${request.page}/${request.pageSize}`
      if (request.mine) label += ' mine'
      if (request.name) label += ` name=${request.name}`
      harness.requests.push(label)
      return answer(harness.server, request)
    },
    placeholderData: keepPreviousData,
  })
  const { table } = useDataTable({
    data: query.data?.items ?? NO_ROWS,
    columns,
    manualPagination: true,
    manualFiltering: true,
    totalCount: query.data?.total ?? 0,
    totalCountCapped: query.data?.capped === true,
    isPlaceholderData: query.isPlaceholderData,
    pagination,
    onPaginationChange: urlState.onPaginationChange,
    columnFilters: urlState.columnFilters,
    onColumnFiltersChange: urlState.onColumnFiltersChange,
    ensurePageInRange: harness.wrapEnsurePageInRange
      ? (pageCount, opts) => urlState.ensurePageInRange(pageCount, opts)
      : urlState.ensurePageInRange,
  })
  return (
    <>
      <button
        type='button'
        onClick={() => table.getColumn('name')?.setFilterValue('even')}
      >
        Filter by name
      </button>
      <ul>
        {table.getRowModel().rows.map((row) => (
          <li key={row.id}>{row.original.name}</li>
        ))}
      </ul>
      <DataTablePagination table={table} />
    </>
  )
}

function RowsPage() {
  const harness = useHarness()
  useSyncExternalStore(harness.appState.subscribe, harness.appState.read)
  const [mine, setMine] = useState(false)
  return (
    <>
      <button type='button' onClick={() => setMine(true)}>
        Only mine
      </button>
      <RowsTable mine={mine} />
    </>
  )
}

const rowsSearchSchema = z.object({
  page: z.number().optional().catch(1),
  pageSize: z.number().optional().catch(undefined),
  name: z.string().optional().catch(''),
})

/** Renders the list at url under a real router, answered by server. */
function renderRows(props: {
  server: Server
  url: string
  wrapEnsurePageInRange?: boolean
}) {
  const appState = createAppState()
  const requests: string[] = []
  const rootRoute = createRootRouteWithContext<{ appState: AppState }>()({
    beforeLoad: ({ context }) => {
      context.appState.write()
    },
  })
  const rowsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/rows',
    validateSearch: rowsSearchSchema,
    component: RowsPage,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([rowsRoute]),
    history: createMemoryHistory({ initialEntries: [props.url] }),
    context: { appState },
  })
  const navigate = vi.spyOn(router, 'navigate')
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <HarnessContext.Provider
      value={{
        server: props.server,
        appState,
        requests,
        wrapEnsurePageInRange: props.wrapEnsurePageInRange ?? false,
      }}
    >
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </HarnessContext.Provider>
  )
  return { router, navigate, requests }
}

beforeEach(() => {
  // jsdom has no scrolling; the router resets the scroll position.
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
})

afterEach(() => {
  localStorage.clear()
})

it('sends a link past the end of a counted list back to the first page once', async () => {
  // 25 rows are 3 pages of 10: page 5 is past the end.
  const { router, navigate, requests } = renderRows({
    server: { matches: 25 },
    url: '/rows?page=5&pageSize=10',
  })

  expect(await screen.findByText('row 1')).toBeVisible()
  expect(requests).toEqual(['5/10', '1/10'])
  expect(router.state.location.search).not.toHaveProperty('page')
  expect(navigate).toHaveBeenCalledOnce()
})

it('sends a link past the end back to the first page again when it is opened a second time', async () => {
  const { router, navigate } = renderRows({
    server: { matches: 25 },
    url: '/rows?page=5&pageSize=10',
  })
  expect(await screen.findByText('row 1')).toBeVisible()

  await act(async () => {
    router.history.push('/rows?page=5&pageSize=10')
  })

  await waitFor(() =>
    expect(router.state.location.search).not.toHaveProperty('page')
  )
  expect(await screen.findByText('row 1')).toBeVisible()
  expect(navigate).toHaveBeenCalledTimes(2)
})

it('sends the open page back to the first once when a view switch leaves it past the end', async () => {
  const user = userEvent.setup()
  // Page 5 of 100 rows exists; the 25 rows of the narrower view are 3 pages.
  const { router, navigate, requests } = renderRows({
    server: { matches: 100, mineMatches: 25 },
    url: '/rows?page=5&pageSize=10',
  })
  expect(await screen.findByText('row 41')).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Only mine' }))

  expect(await screen.findByText('row 1')).toBeVisible()
  expect(requests).toEqual(['5/10', '5/10 mine', '1/10 mine'])
  expect(router.state.location.search).not.toHaveProperty('page')
  expect(navigate).toHaveBeenCalledOnce()
})

it('starts a filtered list on its first page with a single navigation', async () => {
  const user = userEvent.setup()
  // Page 3 of 25 rows; the filter matches 4 rows.
  const { router, navigate, requests } = renderRows({
    server: { matches: 25, filteredMatches: 4 },
    url: '/rows?page=3&pageSize=10',
  })
  expect(await screen.findByText('row 21')).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Filter by name' }))

  expect(await screen.findByText('row 4')).toBeVisible()
  expect(requests.at(-1)).toBe('1/10 name=even')
  expect(router.state.location.search).toEqual({ pageSize: 10, name: 'even' })
  expect(navigate).toHaveBeenCalledOnce()
})

it('sends the open page back to the first once when a larger page size leaves it past the end', async () => {
  // Page 3 of 25 rows holds rows 21-25 at 10 a page; at 50 a page there is
  // a single page.
  const { router, navigate, requests } = renderRows({
    server: { matches: 25 },
    url: '/rows?page=3&pageSize=10',
  })
  expect(await screen.findByText('row 21')).toBeVisible()

  await act(async () => {
    router.history.push('/rows?page=3&pageSize=50')
  })

  expect(await screen.findByText('row 1')).toBeVisible()
  await waitFor(() => expect(requests.at(-1)).toBe('1/50'))
  expect(router.state.location.search).toEqual({ pageSize: 50 })
  expect(navigate).toHaveBeenCalledOnce()
})

it('sends a link past the end of a capped list back to the first page once', async () => {
  // The count stops at 50; 120 rows are 12 pages of 10: page 20 is empty.
  const { router, navigate, requests } = renderRows({
    server: { matches: 120, cap: 50 },
    url: '/rows?page=20&pageSize=10',
  })

  expect(await screen.findByText('row 1')).toBeVisible()
  expect(requests).toEqual(['20/10', '1/10'])
  expect(router.state.location.search).not.toHaveProperty('page')
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeEnabled()
  expect(navigate).toHaveBeenCalledOnce()
})

it('goes back once to the last full page of a capped list when the next page is empty', async () => {
  const user = userEvent.setup()
  // The count stops at 50; 60 rows fill exactly 6 pages of 10.
  const { router, navigate, requests } = renderRows({
    server: { matches: 60, cap: 50 },
    url: '/rows?page=6&pageSize=10',
  })
  expect(await screen.findByText('row 51')).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Go to next page' }))

  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Go to next page' })
    ).toBeDisabled()
  )
  expect(requests.slice(0, 2)).toEqual(['6/10', '7/10'])
  expect(router.state.location.search).toEqual({ page: 6, pageSize: 10 })
  expect(screen.getByText('row 51')).toBeVisible()
  // The click on next, then one step back to page 6.
  expect(navigate).toHaveBeenCalledTimes(2)
})

it('sends a page past the end back to the first once when the table gets a new callback on each render', async () => {
  const { router, navigate, requests } = renderRows({
    server: { matches: 25 },
    url: '/rows?page=5&pageSize=10',
    wrapEnsurePageInRange: true,
  })

  expect(await screen.findByText('row 1')).toBeVisible()
  expect(requests).toEqual(['5/10', '1/10'])
  expect(router.state.location.search).not.toHaveProperty('page')
  expect(navigate).toHaveBeenCalledOnce()
})
