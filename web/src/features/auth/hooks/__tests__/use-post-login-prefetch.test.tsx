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
  lazyRouteComponent,
  Outlet,
  redirect,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { z } from 'zod'

import { api } from '@/lib/api'
import { IDLE_PREFETCH_DELAY_MS } from '@/lib/idle-prefetch'
import { useAuthStore } from '@/stores/auth-store'

import { SignIn } from '../../sign-in'
import { SignUp } from '../../sign-up'

type Page = { default: () => null }

const page = (): Promise<Page> => Promise.resolve({ default: () => null })

const clients: QueryClient[] = []
// Every request the pages send, in order.
let requests: string[] = []

function recordRequest(method: string) {
  return async (url: string) => {
    requests.push(`${method} ${url}`)
    if (method === 'GET' && url === '/api/status') {
      return { data: { success: true, data: {} } }
    }
    throw new Error(`Unexpected ${method} ${url}`)
  }
}

/**
 * The sign-in and sign-up pages in a router shaped like the app's: the
 * console pages sit behind a layout route with a session guard, and each one
 * is a lazy chunk as in a production build. The spies record each guard,
 * loader and download.
 */
async function renderPage(path: string) {
  const spies = {
    guard: vi.fn(),
    loader: vi.fn(),
    layout: vi.fn(page),
    dashboard: vi.fn(page),
    keys: vi.fn(page),
  }
  const root = createRootRoute({ component: Outlet })
  const auth = createRoute({ getParentRoute: () => root, id: '(auth)' })
  const authPages = [
    createRoute({
      getParentRoute: () => auth,
      path: 'sign-in',
      validateSearch: z.object({ redirect: z.string().optional() }),
      component: SignIn,
    }),
    createRoute({
      getParentRoute: () => auth,
      path: 'sign-up',
      component: SignUp,
    }),
    createRoute({
      getParentRoute: () => auth,
      path: 'forgot-password',
      component: () => <h2>Forgot password</h2>,
    }),
  ]
  const layout = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
    beforeLoad: spies.guard,
    loader: spies.loader,
    component: lazyRouteComponent(spies.layout),
  })
  const consolePages = [
    createRoute({
      getParentRoute: () => layout,
      path: 'dashboard/',
      beforeLoad: () => {
        spies.guard()
        throw redirect({
          to: '/dashboard/$section',
          params: { section: 'overview' },
        })
      },
    }),
    createRoute({
      getParentRoute: () => layout,
      path: 'dashboard/$section',
      beforeLoad: spies.guard,
      loader: spies.loader,
      component: lazyRouteComponent(spies.dashboard),
    }),
    createRoute({
      getParentRoute: () => layout,
      path: 'keys',
      beforeLoad: spies.guard,
      loader: spies.loader,
      component: lazyRouteComponent(spies.keys),
    }),
  ]
  const router = createRouter({
    routeTree: root.addChildren([
      auth.addChildren(authPages),
      layout.addChildren(consolePages),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  await router.load()
  await act(async () => {
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
  })
  // Let the page's own requests settle, well before the prefetch delay.
  await act(() => vi.advanceTimersByTimeAsync(50))
  expect(
    screen.getByRole('heading', {
      level: 2,
      name: /^(Sign in|Create an account)$/,
    })
  ).toBeInTheDocument()
  return { router, spies }
}

/** Advance the fake clock past the prefetch delay. */
async function waitPastPrefetchDelay() {
  await act(() => vi.advanceTimersByTimeAsync(IDLE_PREFETCH_DELAY_MS * 5))
}

function setConnection(connection: object | undefined) {
  Object.defineProperty(navigator, 'connection', {
    configurable: true,
    value: connection,
  })
}

beforeEach(() => {
  // Only the timeouts, and the clock moves only when a test moves it. The
  // testing library's async helpers wait on a timeout, so the tests use act()
  // and the fake clock instead.
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  requests = []
  vi.spyOn(api, 'get').mockImplementation(recordRequest('GET'))
  vi.spyOn(api, 'post').mockImplementation(recordRequest('POST'))
  vi.spyOn(api, 'put').mockImplementation(recordRequest('PUT'))
  vi.spyOn(api, 'patch').mockImplementation(recordRequest('PATCH'))
  vi.spyOn(api, 'delete').mockImplementation(recordRequest('DELETE'))
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: unknown) => {
      requests.push(`fetch ${String(input)}`)
      throw new Error('Unexpected fetch')
    })
  )
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  setConnection(undefined)
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  localStorage.clear()
})

describe('sign-in page', () => {
  it('downloads the code of the dashboard once the page has settled, with no request, guard or loader', async () => {
    const { router, spies } = await renderPage('/sign-in')
    const pageRequests = [...requests]

    await act(() => vi.advanceTimersByTimeAsync(IDLE_PREFETCH_DELAY_MS - 100))
    expect(spies.layout).not.toHaveBeenCalled()
    expect(spies.dashboard).not.toHaveBeenCalled()

    await act(() => vi.advanceTimersByTimeAsync(100))
    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.dashboard).toHaveBeenCalledTimes(1)
    expect(spies.keys).not.toHaveBeenCalled()

    await waitPastPrefetchDelay()
    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.dashboard).toHaveBeenCalledTimes(1)
    expect(spies.guard).not.toHaveBeenCalled()
    expect(spies.loader).not.toHaveBeenCalled()
    expect(requests).toEqual(pageRequests)
    expect(router.state.location.href).toBe('/sign-in')
    expect(useAuthStore.getState().auth).toMatchObject({
      user: null,
      accessToken: null,
      session: null,
      pendingLoginVerification: null,
    })
    expect(
      screen.getByRole('heading', { level: 2, name: 'Sign in' })
    ).toBeInTheDocument()
  })

  it('waits for the browser to be idle', async () => {
    let idle: (() => void) | undefined
    vi.stubGlobal('requestIdleCallback', (callback: () => void) => {
      idle = callback
      return 1
    })
    vi.stubGlobal('cancelIdleCallback', vi.fn())
    const { spies } = await renderPage('/sign-in')

    await waitPastPrefetchDelay()
    expect(idle).toBeTypeOf('function')
    expect(spies.dashboard).not.toHaveBeenCalled()

    act(() => idle?.())
    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.dashboard).toHaveBeenCalledTimes(1)
  })

  it('downloads the page a safe redirect target opens', async () => {
    const { spies } = await renderPage('/sign-in?redirect=%2Fkeys%3Fpage%3D2')

    await waitPastPrefetchDelay()

    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.keys).toHaveBeenCalledTimes(1)
    expect(spies.dashboard).not.toHaveBeenCalled()
  })

  it.each([
    'https://evil.example/keys',
    '//evil.example/keys',
    'javascript:alert(1)',
  ])(
    'downloads the dashboard instead of the unsafe redirect target %s',
    async (target) => {
      const { spies } = await renderPage(
        `/sign-in?redirect=${encodeURIComponent(target)}`
      )

      await waitPastPrefetchDelay()

      expect(spies.dashboard).toHaveBeenCalledTimes(1)
      expect(spies.keys).not.toHaveBeenCalled()
    }
  )

  it.each([
    { connection: { saveData: true }, why: 'asks to save data' },
    { connection: { effectiveType: '2g' }, why: 'reports a 2G connection' },
  ])('downloads nothing when the browser $why', async ({ connection }) => {
    setConnection(connection)
    const { spies } = await renderPage('/sign-in')

    await waitPastPrefetchDelay()

    expect(spies.layout).not.toHaveBeenCalled()
    expect(spies.dashboard).not.toHaveBeenCalled()
  })

  it('downloads nothing when the page is left before the delay', async () => {
    const { router, spies } = await renderPage('/sign-in')

    await act(() => router.navigate({ to: '/forgot-password' }))
    expect(
      screen.getByRole('heading', { name: 'Forgot password' })
    ).toBeInTheDocument()
    await waitPastPrefetchDelay()

    expect(spies.layout).not.toHaveBeenCalled()
    expect(spies.dashboard).not.toHaveBeenCalled()
  })
})

describe('sign-up page', () => {
  // A WeChat sign-in from this page goes straight to the dashboard.
  it('downloads the code of the dashboard once the page has settled', async () => {
    const { spies } = await renderPage('/sign-up')
    const pageRequests = [...requests]

    await waitPastPrefetchDelay()

    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.dashboard).toHaveBeenCalledTimes(1)
    expect(spies.guard).not.toHaveBeenCalled()
    expect(spies.loader).not.toHaveBeenCalled()
    expect(requests).toEqual(pageRequests)
  })
})
