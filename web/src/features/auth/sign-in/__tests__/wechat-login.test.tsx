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
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { UserAuthForm } from '../components/user-auth-form'

const clients: QueryClient[] = []
const limitMessageKey =
  'Too many registration attempts from your network. Please try again in {{minutes}} minutes.'

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
})

async function renderSignIn() {
  const root = createRootRoute({ component: Outlet })
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({
        getParentRoute: () => root,
        path: '/sign-in',
        component: () => <UserAuthForm />,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/forgot-password',
        component: () => <div>Forgot password page</div>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ['/sign-in'] }),
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
}

it('shows the network limit message when the server refuses a new WeChat account', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      return { data: { success: true, data: { wechat_login: true } } }
    }
    if (url === '/api/oauth/wechat') {
      // WeChat sign-ups count toward the per-network registration limit.
      throw {
        isAxiosError: true,
        message: 'Request failed with status code 429',
        response: {
          status: 429,
          data: {
            success: false,
            code: 'REGISTER_RATE_LIMITED',
            message:
              'Too many registration attempts from your network. Please try again in 15 minutes.',
            message_key: limitMessageKey,
            message_params: { minutes: 15 },
          },
        },
      }
    }
    throw new Error(`Unexpected GET ${url}`)
  })
  const error = vi.spyOn(toast, 'error')
  await renderSignIn()

  await user.click(
    await screen.findByRole('button', { name: /Continue with WeChat/ })
  )
  await user.type(await screen.findByLabelText('Verification code'), 'wx-code')
  await user.click(screen.getByRole('button', { name: 'Confirm' }))

  await waitFor(() =>
    expect(error).toHaveBeenCalledWith(
      i18next.t(limitMessageKey, { minutes: 15 })
    )
  )
  expect(error.mock.calls[0][0]).toContain('15')
})
