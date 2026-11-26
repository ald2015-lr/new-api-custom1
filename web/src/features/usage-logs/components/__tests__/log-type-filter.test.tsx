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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

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
import { Route as UsageLogsRoute } from '@/routes/_authenticated/usage-logs/$section'
import { type AuthUser, useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { LOG_TYPE_ENUM } from '../../constants'
import { usageLogSchema } from '../../data/schema'
import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'
import { UsageLogsTable } from '../usage-logs-table'

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

function ScopedLogsTable() {
  const { viewScope, setViewScope } = useUsageLogsContext()
  return (
    <>
      <button
        type='button'
        onClick={() => setViewScope(viewScope === 'all' ? 'self' : 'all')}
      >
        Switch scope
      </button>
      <UsageLogsTable logCategory='common' />
    </>
  )
}

function TableFixture() {
  return (
    <UsageLogsProvider>
      <ScopedLogsTable />
    </UsageLogsProvider>
  )
}

const consumeLog = usageLogSchema.parse({
  id: 1,
  user_id: 2,
  created_at: 1788840000,
  type: 2,
  content: '',
  quota: 5000,
})

const moderationLog = usageLogSchema.parse({
  id: 2,
  user_id: 2,
  created_at: 1788840000,
  type: 100,
  content: 'Rejected by content moderation',
  model_name: 'gpt-moderated',
  quota: 0,
  use_time: 1,
  other: JSON.stringify({
    content_parts: [{ key: 'Rejected by content moderation' }],
  }),
})

async function renderFilter(
  options: { user?: AuthUser; path?: string; table?: boolean } = {}
) {
  if (options.user) useAuthStore.getState().auth.setUser(options.user)
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = { quota: 0, rpm: 0, tpm: 0 }
    if (url === '/api/user/self/groups') data = {}
    if (url === '/api/group/') data = []
    if (url === '/api/subscription/plans') data = []
    if (url === '/api/subscription/self') data = { subscriptions: [] }
    if (url.startsWith('/api/log/?') || url.startsWith('/api/log/self?')) {
      // Like the server, which returns no row for a type it filters out.
      const type = new URLSearchParams(url.split('?')[1]).get('type')
      let items = type === '1' ? [] : [consumeLog]
      if (type === '100') items = [moderationLog]
      data = { items, total: items.length }
    }
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: options.table ? TableFixture : FilterFixture,
    // The page's own search schema, so a type it does not know is dropped
    // here as it is in the console.
    validateSearch: UsageLogsRoute.options.validateSearch,
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
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

function lastLogRequest(prefix: string) {
  const urls = vi
    .mocked(api.get)
    .mock.calls.map(([url]) => url)
    .filter((url) => url.startsWith(prefix))
  return new URLSearchParams(urls.at(-1)?.split('?')[1])
}

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

// billingRead varies on its own: root can untick recharge permissions on a
// full admin, and the filter follows the capability, not the admin kind.
function adminViewer(
  adminRole: 'admin' | 'deputy',
  billingRead = adminRole === 'admin'
): AuthUser {
  return {
    id: 7,
    username: adminRole,
    role: ROLE.ADMIN,
    admin_role: adminRole,
    permissions: {
      admin_permissions: {
        billing: { read: billingRead, write: billingRead },
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

it.each([
  ['a deputy admin', adminViewer('deputy')],
  [
    'an admin whose recharge permissions root removed',
    adminViewer('admin', false),
  ],
])(
  "%s listing every user's logs is offered neither Top-up nor Manage",
  async (_kind, user) => {
    await renderFilter({ user })

    const names = await typeOptionNames()
    expect(names).not.toContain('Top-up')
    expect(names.some((name) => name.startsWith('Manage'))).toBe(false)
    expect(names).toContain('Consume')
    expect(names.some((name) => name.startsWith('Login'))).toBe(true)
  }
)

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

it("a deputy admin who filtered their own Top-up logs and switches to every user's logs gets all types listed", async () => {
  const router = await renderFilter({
    user: adminViewer('deputy'),
    table: true,
  })
  await userEvent.click(screen.getByRole('button', { name: 'Switch scope' }))
  await userEvent.click(screen.getByRole('combobox', { name: 'Type' }))
  await userEvent.click(screen.getByRole('option', { name: 'Top-up' }))
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(lastLogRequest('/api/log/self?').get('type')).toBe('1')
  )

  await userEvent.click(screen.getByRole('button', { name: 'Switch scope' }))

  expect(await screen.findByText('$0.01')).toBeVisible()
  expect(lastLogRequest('/api/log/?').get('type')).toBe('0')
  expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent(
    'All Types'
  )
  expect(router.state.location.search).toMatchObject({ type: ['1'] })

  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ type: ['0'] })
  )
})

it("an admin opening a Top-up logs link of every user's logs keeps asking for Top-up", async () => {
  await renderFilter({
    user: adminViewer('admin'),
    table: true,
    path: '/usage-logs/common?type=%5B%221%22%5D',
  })

  await waitFor(() =>
    expect(lastLogRequest('/api/log/?').get('type')).toBe('1')
  )
  expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent(
    'Top-up'
  )
})

it.each([
  ['an admin', adminViewer('admin'), '/api/log/?'],
  ['a deputy admin without billing read', adminViewer('deputy'), '/api/log/?'],
])(
  '%s is offered Moderation and selecting it asks for type 100',
  async (_kind, user, endpoint) => {
    const router = await renderFilter({ user, table: true })
    expect(await typeOptionNames()).toContain('Moderation')

    await userEvent.click(screen.getByRole('combobox', { name: 'Type' }))
    await userEvent.click(screen.getByRole('option', { name: 'Moderation' }))
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))

    await waitFor(() =>
      expect(lastLogRequest(endpoint).get('type')).toBe('100')
    )
    expect(router.state.location.search).toMatchObject({ type: ['100'] })
  }
)

it('Moderation filters on the type the server stores for a moderation rejection', () => {
  const source = readFileSync(
    resolve(
      import.meta.dirname,
      '../../../../../..',
      'model/log_moderation.go'
    ),
    'utf8'
  )
  const value = /^const LogTypeModeration = (\d+)$/m.exec(source)?.[1]
  expect(Number(value)).toBe(LOG_TYPE_ENUM.MODERATION)
})

it('a user viewing their own logs is offered Moderation', async () => {
  await renderFilter({ user: { id: 3, username: 'user', role: ROLE.USER } })
  expect(await typeOptionNames()).toContain('Moderation')
})

it('opening a Moderation logs link keeps the type and asks for type 100', async () => {
  const router = await renderFilter({
    user: adminViewer('admin'),
    table: true,
    path: '/usage-logs/common?type=%5B%22100%22%5D',
  })

  await waitFor(() =>
    expect(lastLogRequest('/api/log/?').get('type')).toBe('100')
  )
  expect(router.state.location.search).toMatchObject({ type: ['100'] })
  expect(screen.getByRole('combobox', { name: 'Type' })).toHaveTextContent(
    'Moderation'
  )
  const details = await screen.findByText('Rejected by content moderation')
  const row = details.closest('tr')
  expect(row).toHaveClass('bg-amber-50/40')
  expect(within(row as HTMLElement).getByText('gpt-moderated')).toBeVisible()
})
