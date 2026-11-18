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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { type AuthUser, useAuthStore } from '@/stores/auth-store'

import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'

function ScopedFilterBar() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  const { viewScope, setViewScope } = useUsageLogsContext()
  return (
    <>
      <button
        type='button'
        onClick={() => setViewScope(viewScope === 'all' ? 'self' : 'all')}
      >
        Switch scope
      </button>
      <CommonLogsFilterBar table={table} />
    </>
  )
}

function FilterFixture() {
  return (
    <UsageLogsProvider>
      <ScopedFilterBar />
    </UsageLogsProvider>
  )
}

async function renderFilter(options: { user?: AuthUser; path?: string } = {}) {
  if (options.user) useAuthStore.getState().auth.setUser(options.user)
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = { quota: 0, rpm: 0, tpm: 0 }
    if (url === '/api/user/self/groups') data = {}
    if (url === '/api/group/') data = []
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: FilterFixture,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/usage-logs/common'],
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
  await screen.findByRole('combobox', { name: 'Type' })
  return router
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('marks only retired log types as deprecated while keeping historical filters selectable', async () => {
  const router = await renderFilter()
  await userEvent.click(screen.getByRole('combobox', { name: 'Type' }))
  for (const label of ['Manage', 'Login']) {
    expect(
      within(
        screen.getByRole('option', { name: new RegExp(`^${label}`) })
      ).getByText('Deprecated')
    ).toBeVisible()
  }
  for (const label of [
    'All Types',
    'Top-up',
    'Consume',
    'System',
    'Error',
    'Refund',
  ]) {
    expect(
      within(screen.getByRole('option', { name: label })).queryByText(
        'Deprecated'
      )
    ).not.toBeInTheDocument()
  }
  await userEvent.click(screen.getByRole('option', { name: /^Manage/ }))
  expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent(
    'Deprecated'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ type: ['3'], page: 1 })
  )
  await userEvent.click(screen.getByRole('combobox', { name: 'Type' }))
  await userEvent.click(screen.getByRole('option', { name: /^Login/ }))
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ type: ['7'], page: 1 })
  )
})

function adminViewer(adminRole: 'admin' | 'deputy'): AuthUser {
  return {
    id: 7,
    username: adminRole,
    role: ROLE.ADMIN,
    admin_role: adminRole,
    permissions: {
      admin_permissions: {
        billing: {
          read: adminRole === 'admin',
          write: adminRole === 'admin',
        },
      },
    },
  }
}

async function typeOptionNames() {
  await userEvent.click(screen.getByRole('combobox', { name: 'Type' }))
  const names = screen
    .getAllByRole('option')
    .map((option) => option.textContent ?? '')
  await userEvent.keyboard('{Escape}')
  return names
}

it("a deputy admin listing every user's logs is offered neither Top-up nor Manage", async () => {
  await renderFilter({ user: adminViewer('deputy') })

  const names = await typeOptionNames()
  expect(names).not.toContain('Top-up')
  expect(names.some((name) => name.startsWith('Manage'))).toBe(false)
  expect(names).toContain('Consume')
  expect(names.some((name) => name.startsWith('Login'))).toBe(true)
})

it('a deputy admin viewing only their own logs is offered Top-up and Manage', async () => {
  await renderFilter({ user: adminViewer('deputy') })

  await userEvent.click(screen.getByRole('button', { name: 'Switch scope' }))

  const names = await typeOptionNames()
  expect(names).toContain('Top-up')
  expect(names.some((name) => name.startsWith('Manage'))).toBe(true)
})

it.each([
  ['an admin', adminViewer('admin')],
  ['root', { id: 1, username: 'root', role: ROLE.SUPER_ADMIN }],
])(
  "%s listing every user's logs is offered Top-up and Manage",
  async (_kind, user) => {
    await renderFilter({ user })

    const names = await typeOptionNames()
    expect(names).toContain('Top-up')
    expect(names.some((name) => name.startsWith('Manage'))).toBe(true)
  }
)

it.each([
  ['a deputy admin', 'All Types', adminViewer('deputy')],
  ['an admin', 'Top-up', adminViewer('admin')],
])(
  '%s opening a Top-up logs link sees %s as the selected type',
  async (_kind, label, user) => {
    await renderFilter({
      user,
      path: '/usage-logs/common?type=%5B%221%22%5D',
    })

    expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent(
      label
    )
  }
)
