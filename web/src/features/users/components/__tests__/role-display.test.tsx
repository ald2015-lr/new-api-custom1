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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { Route as UsersRoute } from '@/routes/_authenticated/users/index'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { UsersProvider } from '../users-provider'
import { UsersTable } from '../users-table'

const clients: QueryClient[] = []

function listedUser(id: number, username: string, extra: Partial<User>) {
  return {
    id,
    username,
    display_name: '',
    role: 1,
    status: 1,
    quota: 0,
    used_quota: 0,
    request_count: 0,
    group: 'default',
    ...extra,
  }
}

const listedUsers = [
  listedUser(2, 'deputy-user', { role: 10, admin_role: 'deputy' }),
  listedUser(3, 'admin-user', { role: 10, admin_role: 'admin' }),
  listedUser(4, 'common-user', {}),
]

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  useAuthStore.getState().auth.reset()
})

async function renderUsers(language: 'en' | 'zh', initialEntry = '/users/') {
  const i18n = createInstance()
  await i18n.init({
    lng: language,
    fallbackLng: false,
    nsSeparator: false,
    resources: { en, zh },
    initAsync: false,
  })
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    return {
      data: {
        success: true,
        data: { items: listedUsers, total: listedUsers.length },
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
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  )
  await screen.findByText('deputy-user')
  return { get, router }
}

// The Role column header is a menu with the same name; the filter chip is
// the one that opens a dialog.
function getRoleFilterTrigger(name: RegExp) {
  const trigger = screen
    .getAllByRole('button', { name })
    .find((button) => button.getAttribute('aria-haspopup') === 'dialog')
  expect(trigger).toBeDefined()
  return trigger as HTMLElement
}

function searchUrl(role: string, adminRole: string) {
  return `/api/user/search?keyword=&group=&role=${role}&admin_role=${adminRole}&p=1&page_size=20`
}

it('labels a deputy admin 副管理员 and a full admin 管理员 in the Chinese role column', async () => {
  await renderUsers('zh')

  const deputyRow = screen.getByRole('row', { name: /deputy-user/ })
  const adminRow = screen.getByRole('row', { name: /admin-user/ })
  expect(within(deputyRow).getByText('副管理员')).toBeInTheDocument()
  expect(within(adminRow).getByText('管理员')).toBeInTheDocument()
  expect(within(adminRow).queryByText('副管理员')).not.toBeInTheDocument()
})

it.each([
  ['Deputy Admin', 'deputy'],
  ['Admin', 'admin'],
])(
  'the %s role filter searches role 10 with admin_role %s',
  async (option, adminRole) => {
    const user = userEvent.setup()
    const { get, router } = await renderUsers('en')

    await user.click(getRoleFilterTrigger(/^Role/))
    await user.click(
      await screen.findByRole('option', { name: new RegExp(`^${option}`) })
    )

    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(searchUrl('10', adminRole))
    )
    expect(router.state.location.search).toMatchObject({
      role: [adminRole === 'deputy' ? 'deputy' : '10'],
    })
  }
)

it('restores the Deputy Admin role filter from the page URL on load', async () => {
  const { get } = await renderUsers('en', '/users/?role=%5B%22deputy%22%5D')

  expect(get).toHaveBeenCalledWith(searchUrl('10', 'deputy'))
  expect(get).not.toHaveBeenCalledWith('/api/user/', expect.anything())
})
