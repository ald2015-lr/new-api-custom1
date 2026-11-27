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
import { QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  AxiosHeaders,
  type AxiosResponse,
  type InternalAxiosRequestConfig,
} from 'axios'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { clearAuthentication } from '@/lib/auth-session'
import { createAppQueryClient } from '@/lib/query-client'
import { Route as authRoute } from '@/routes/(auth)/route'
import { Route as signInRoute } from '@/routes/(auth)/sign-in'
import { Route as rootRoute } from '@/routes/__root'
import { Route as pricingModelRoute } from '@/routes/pricing/$modelId/index'
import { Route as pricingRoute } from '@/routes/pricing/index'
import { useAuthStore } from '@/stores/auth-store'

// The model square on the app's own routes (root guard, sign-in guard,
// /pricing, /pricing/$modelId) and a memory-history router, so a redirect to
// the sign-in page and its way back happen as in the browser. Every axios
// client, the session refresh included, answers from `server`.

type Reply = {
  status: number
  data?: unknown
  headers?: Record<string, string>
}

const server = vi.hoisted(() => ({
  reply: undefined as
    | undefined
    | ((config: InternalAxiosRequestConfig) => Reply),
}))

vi.mock('axios', async (importOriginal) => {
  const actual = await importOriginal<typeof import('axios')>()
  actual.default.defaults.adapter = async (config) => {
    const reply = server.reply?.(config) ?? { status: 404 }
    const response: AxiosResponse = {
      data: reply.data,
      status: reply.status,
      statusText: String(reply.status),
      headers: new actual.AxiosHeaders(reply.headers ?? {}),
      config,
      request: {},
    }
    if (!config.validateStatus || config.validateStatus(reply.status)) {
      return response
    }
    throw new actual.AxiosError(
      `Request failed with status code ${reply.status}`,
      reply.status >= 500 ? 'ERR_BAD_RESPONSE' : 'ERR_BAD_REQUEST',
      config,
      {},
      response
    )
  }
  return actual
})

const SESSION = {
  sid: 'sid-1',
  current: true,
  login_method: 'password',
  ip: '',
  user_agent: '',
  created_at: 1,
  last_active_at: 1,
  expires_at: 9_999_999_999,
}
const USER = { id: 7, username: 'alice', role: 1 }

const STATUS = {
  setup: true,
  system_name: 'Test',
  HeaderNavModules: JSON.stringify({
    pricing: { enabled: true, requireAuth: false },
  }),
}

const PRICING = {
  success: true,
  data: [
    {
      model_name: 'listed-model',
      quota_type: 0,
      model_ratio: 1,
      model_price: 0,
      completion_ratio: 1,
      enable_groups: ['default'],
      supported_endpoint_types: ['openai'],
    },
  ],
  vendors: [],
  group_ratio: { default: 1 },
  usable_group: { default: { desc: 'Default', ratio: 1 } },
  supported_endpoint: {},
  auto_groups: [],
}

const SESSION_HINT_COOKIE = 'new_api_has_session'

function routeTree() {
  // As in routeTree.gen.ts, with only the routes these tests visit.
  const auth = authRoute.update({
    id: '/(auth)',
    getParentRoute: () => rootRoute,
  } as never)
  const signIn = signInRoute.update({
    id: '/sign-in',
    path: '/sign-in',
    getParentRoute: () => auth,
  } as never)
  const pricing = pricingRoute.update({
    id: '/pricing/',
    path: '/pricing/',
    getParentRoute: () => rootRoute,
  } as never)
  const pricingModel = pricingModelRoute.update({
    id: '/pricing/$modelId/',
    path: '/pricing/$modelId/',
    getParentRoute: () => rootRoute,
  } as never)
  const home = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <p>home page</p>,
  })
  return rootRoute.addChildren([
    home,
    auth.addChildren([signIn]),
    pricing,
    pricingModel,
  ])
}

function mountApp() {
  const queryClient = createAppQueryClient(() => undefined)
  const router = createRouter({
    routeTree: routeTree(),
    history: createMemoryHistory({ initialEntries: ['/'] }),
    context: { queryClient },
  })
  const visited: string[] = []
  router.subscribe('onBeforeNavigate', (event) => {
    visited.push(event.toLocation.pathname)
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>
  )
  return { router, visited }
}

function authorization(config: InternalAxiosRequestConfig): string {
  return String(
    AxiosHeaders.from(config.headers as never).get('Authorization') ?? ''
  )
}

function statusReply(config: InternalAxiosRequestConfig): Reply {
  if (config.url === '/api/status') {
    return { status: 200, data: { success: true, data: STATUS } }
  }
  if (config.url === '/api/setup') {
    return { status: 200, data: { success: true, data: { status: true } } }
  }
  if (config.url?.startsWith('/api/perf-metrics')) {
    return { status: 200, data: { success: true, data: { groups: [] } } }
  }
  return { status: 200, data: { success: true, data: null } }
}

/** Signed in earlier; the 15-minute access token has run out since. */
function signInWithExpiredToken() {
  document.cookie = `${SESSION_HINT_COOKIE}=1; path=/`
  useAuthStore.setState({
    auth: {
      ...useAuthStore.getState().auth,
      user: USER,
      session: SESSION,
      accessToken: 'expired-access-token',
      accessExpiresAt: 1,
      bootstrapState: 'complete',
    },
  })
}

beforeEach(() => {
  clearAuthentication(false, 'idle')
  window.localStorage.clear()
  window.sessionStorage.clear()
})

afterEach(() => {
  cleanup()
  server.reply = undefined
  document.cookie = `${SESSION_HINT_COOKIE}=; path=/; max-age=0`
  clearAuthentication(false, 'idle')
})

describe('model square and the session', () => {
  test.each([
    { page: 'list', to: '/pricing' },
    { page: 'details page', to: '/pricing/listed-model' },
  ])(
    'a signed-in visitor whose session cannot be refreshed for a moment stays on the $page, which offers a retry',
    async ({ to }) => {
      const user = userEvent.setup()
      signInWithExpiredToken()
      let refreshWorks = false
      let pricingRequests = 0
      server.reply = (config) => {
        if (config.url === '/api/user/auth/refresh') {
          if (!refreshWorks) {
            return {
              status: 429,
              headers: { 'Retry-After': '1' },
              data: { success: false, message: 'Too many requests' },
            }
          }
          return {
            status: 200,
            data: {
              success: true,
              data: {
                access_token: 'fresh-token',
                token_type: 'Bearer',
                access_expires_at: Math.floor(Date.now() / 1000) + 900,
                user: USER,
                session: SESSION,
              },
            },
          }
        }
        if (config.url === '/api/pricing') {
          pricingRequests += 1
          if (authorization(config) === 'Bearer fresh-token') {
            return { status: 200, data: PRICING }
          }
          return {
            status: 401,
            data: {
              success: false,
              code: 'AUTH_TOKEN_EXPIRED',
              message: 'access token expired',
            },
          }
        }
        return statusReply(config)
      }

      const { router, visited } = mountApp()
      await screen.findByText('home page')
      await act(() => router.navigate({ to }))

      expect(
        await screen.findByText('Failed to load model pricing')
      ).toBeVisible()
      expect(visited).not.toContain('/sign-in')
      expect(router.state.location.pathname).toBe(to)
      expect(pricingRequests).toBe(1)

      // The refresh works again: Retry refreshes the session and loads.
      refreshWorks = true
      await user.click(screen.getByRole('button', { name: 'Retry' }))

      expect(
        (await screen.findAllByText('listed-model')).length
      ).toBeGreaterThan(0)
      expect(visited).not.toContain('/sign-in')
    }
  )

  test('an anonymous visitor the server asks to sign in lands on the sign-in page, with the way back', async () => {
    server.reply = (config) => {
      if (config.url === '/api/user/auth/refresh') {
        return {
          status: 401,
          data: { success: false, code: 'AUTH_REFRESH_MISSING' },
        }
      }
      if (config.url === '/api/pricing') {
        return {
          status: 401,
          data: { success: false, message: 'sign-in required' },
        }
      }
      return statusReply(config)
    }

    const { router } = mountApp()
    await screen.findByText('home page')
    await act(() => router.navigate({ to: '/pricing' }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
    expect(router.state.location.search).toEqual({ redirect: '/pricing' })
  })
})
