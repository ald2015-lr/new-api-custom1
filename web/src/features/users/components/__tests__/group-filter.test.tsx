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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { Route as UsersRoute } from '@/routes/_authenticated/users/index'
import { useAuthStore } from '@/stores/auth-store'

import { UsersProvider } from '../users-provider'
import { UsersTable } from '../users-table'

const clients: QueryClient[] = []
const premiumSearchUrl =
  '/api/user/search?keyword=&group=premium&p=1&page_size=20'

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  useAuthStore.getState().auth.reset()
})

async function renderUsers(initialEntry = '/users/') {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default', 'premium'] } }
    }
    return {
      data: {
        success: true,
        data: {
          items: [
            {
              id: 2,
              username: 'premium-user',
              display_name: '',
              role: 1,
              status: 1,
              quota: 0,
              used_quota: 0,
              request_count: 0,
              group: 'premium',
              inviter_id: 42,
              invite_count: 3,
            },
          ],
          total: 1,
        },
      },
    }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const users = createRoute({
    getParentRoute: () => auth,
    path: 'users/',
    validateSearch: UsersRoute.options.validateSearch,
    component: () => (
      <TooltipProvider>
        <UsersProvider>
          <UsersTable />
        </UsersProvider>
      </TooltipProvider>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([users])]),
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByText('premium-user')
  return { get, router }
}

// The User Group column header is a sort menu with the same name; the
// filter chip is the one that opens a dialog.
function getGroupFilterTrigger() {
  const trigger = screen
    .getAllByRole('button', { name: /^User Group/ })
    .find((button) => button.getAttribute('aria-haspopup') === 'dialog')
  expect(trigger).toBeDefined()
  return trigger as HTMLElement
}

it('searches only the selected user group and lists all users again after the filter is cleared', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderUsers()
  expect(get).toHaveBeenCalledWith('/api/user/', {
    params: { p: 1, page_size: 20 },
  })

  await user.click(getGroupFilterTrigger())
  await user.click(await screen.findByRole('option', { name: /^premium/ }))

  await waitFor(() => expect(get).toHaveBeenLastCalledWith(premiumSearchUrl))
  expect(router.state.location.search).toMatchObject({ group: ['premium'] })
  expect(getGroupFilterTrigger()).toHaveTextContent('premium')

  await user.click(screen.getByRole('option', { name: 'Clear filters' }))

  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/user/', {
      params: { p: 1, page_size: 20 },
    })
  )
  expect(router.state.location.search).not.toHaveProperty('group')
})

it('restores the user group filter from the page URL on load', async () => {
  const { get } = await renderUsers('/users/?group=%5B%22premium%22%5D')

  expect(get).toHaveBeenCalledWith(premiumSearchUrl)
  expect(get).not.toHaveBeenCalledWith('/api/user/', expect.anything())
  expect(getGroupFilterTrigger()).toHaveTextContent('premium')
})

const inviterSearchUrl = (inviterId: number) =>
  `/api/user/search?keyword=&group=&inviter_id=${inviterId}&p=1&page_size=20`

it.each([
  ['a link', '/users/?inviter=%2242%22'],
  ['a hand-written URL', '/users/?inviter=42'],
])('restores the inviter filter from %s on load', async (_source, entry) => {
  const { get } = await renderUsers(entry)

  expect(get).toHaveBeenCalledWith(inviterSearchUrl(42))
  expect(get).not.toHaveBeenCalledWith('/api/user/', expect.anything())
  expect(
    screen.getByRole('textbox', { name: 'Filter by inviter ID...' })
  ).toHaveValue('42')
})

it('searches by a typed inviter ID and lists all users again after it is cleared', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderUsers()
  const input = screen.getByRole('textbox', { name: 'Filter by inviter ID...' })

  await user.type(input, '4a2')

  expect(input).toHaveValue('42')
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(inviterSearchUrl(42))
  )
  expect(router.state.location.search).toMatchObject({ inviter: '42' })

  await user.clear(input)

  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/user/', {
      params: { p: 1, page_size: 20 },
    })
  )
  expect(router.state.location.search).not.toHaveProperty('inviter')
})

it('searches by the ID that a typed inviter ID with leading zeros names and drops the zeros once the field is left', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderUsers()
  const input = screen.getByRole('textbox', { name: 'Filter by inviter ID...' })

  await user.type(input, '042')

  expect(input).toHaveValue('042')
  expect(input).toHaveAttribute('aria-invalid', 'false')
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(inviterSearchUrl(42))
  )

  await user.tab()

  expect(input).toHaveValue('42')
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ inviter: '42' })
  )
})

it('keeps the other digits when the first digit of an inviter ID is retyped', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderUsers()
  const input = screen.getByRole('textbox', { name: 'Filter by inviter ID...' })
  await user.type(input, '1005')

  await user.keyboard('{Home}{ArrowRight}{Backspace}')
  expect(input).toHaveValue('005')
  await user.keyboard('2')

  expect(input).toHaveValue('2005')
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(inviterSearchUrl(2005))
  )
  expect(router.state.location.search).toMatchObject({ inviter: '2005' })
})

it('marks an inviter ID above 2147483647 invalid and keeps listing every user', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderUsers()
  const input = screen.getByRole('textbox', { name: 'Filter by inviter ID...' })

  await user.type(input, '2147483648')

  expect(input).toHaveAttribute('aria-invalid', 'true')
  expect(input).toHaveAccessibleDescription('Invalid user ID')
  // The hint hangs below the field instead of making the toolbar row taller,
  // so the field stays in line with the other filters.
  expect(screen.getByText('Invalid user ID')).toHaveClass('sm:absolute')
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      inviter: '2147483648',
    })
  )
  expect(get).not.toHaveBeenCalledWith(expect.stringContaining('inviter_id'))

  await user.type(input, '{Backspace}')

  expect(input).toHaveAttribute('aria-invalid', 'false')
  expect(screen.queryByText('Invalid user ID')).not.toBeInTheDocument()
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(inviterSearchUrl(214748364))
  )
})

it('marks an inviter ID from the page URL that names no user invalid and lists every user', async () => {
  const { get } = await renderUsers(
    '/users/?inviter=%2299999999999999999999%22'
  )

  const input = screen.getByRole('textbox', { name: 'Filter by inviter ID...' })
  expect(input).toHaveValue('99999999999999999999')
  expect(input).toHaveAttribute('aria-invalid', 'true')
  expect(input).toHaveAccessibleDescription('Invalid user ID')
  expect(get).toHaveBeenCalledWith('/api/user/', {
    params: { p: 1, page_size: 20 },
  })
  expect(get).not.toHaveBeenCalledWith(expect.stringContaining('inviter_id'))
})

it.each([
  ['Inviter ID: 42', 42],
  ['Invited 3 users', 2],
])(
  'clicking %s lists the users invited by user %i',
  async (linkName, inviterId) => {
    const user = userEvent.setup()
    const { get, router } = await renderUsers('/users/?filter=premium')

    await user.click(screen.getByRole('button', { name: linkName }))

    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(inviterSearchUrl(inviterId))
    )
    expect(router.state.location.search).toMatchObject({
      inviter: String(inviterId),
    })
    expect(router.state.location.search).not.toHaveProperty('filter')
    expect(
      screen.getByRole('textbox', { name: 'Filter by inviter ID...' })
    ).toHaveValue(String(inviterId))
  }
)

it('reset clears an inviter filter set from the table', async () => {
  const user = userEvent.setup()
  const { get, router } = await renderUsers()
  await user.click(screen.getByRole('button', { name: 'Inviter ID: 42' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(inviterSearchUrl(42))
  )

  await user.click(screen.getByRole('button', { name: 'Reset' }))

  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith('/api/user/', {
      params: { p: 1, page_size: 20 },
    })
  )
  expect(router.state.location.search).not.toHaveProperty('inviter')
  expect(
    screen.getByRole('textbox', { name: 'Filter by inviter ID...' })
  ).toHaveValue('')
})
