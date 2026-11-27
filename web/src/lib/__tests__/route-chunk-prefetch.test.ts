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
  createLazyRoute,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
} from '@tanstack/react-router'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { prefetchRouteChunks } from '../route-chunk-prefetch'

type Page = { default: () => null }

const page = (): Promise<Page> => Promise.resolve({ default: () => null })

/** A failed chunk download, as the rspack chunk loader rejects it. */
function chunkLoadError(chunk: string) {
  return Object.assign(new Error(`Loading chunk ${chunk} failed.`), {
    name: 'ChunkLoadError',
    request: `http://localhost/static/js/async/${chunk}.js`,
  })
}

/**
 * A router shaped like the app's: the sign-in page, and console pages behind
 * a layout route with a session guard. Every page is a lazy chunk, as in a
 * production build; the spies record each guard, loader and download.
 */
function createTestRouter(
  load: {
    section?: () => Promise<Page>
    keys?: () => Promise<{ options: object }>
  } = {}
) {
  const spies = {
    guard: vi.fn(),
    loader: vi.fn(),
    indexGuard: vi.fn(() => {
      throw redirect({
        to: '/dashboard/$section',
        params: { section: 'overview' },
      })
    }),
    layout: vi.fn(page),
    section: vi.fn(load.section ?? page),
    keys: vi.fn(
      load.keys ??
        (() =>
          Promise.resolve(
            createLazyRoute('/_authenticated/keys/')({ component: () => null })
          ))
    ),
  }
  const root = createRootRoute({ component: Outlet })
  const auth = createRoute({ getParentRoute: () => root, id: '(auth)' })
  const signIn = createRoute({
    getParentRoute: () => auth,
    path: 'sign-in',
    component: () => null,
  })
  const layout = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
    beforeLoad: spies.guard,
    loader: spies.loader,
    component: lazyRouteComponent(spies.layout),
  })
  const dashboardIndex = createRoute({
    getParentRoute: () => layout,
    path: 'dashboard/',
    beforeLoad: spies.indexGuard,
  })
  const dashboardSection = createRoute({
    getParentRoute: () => layout,
    path: 'dashboard/$section',
    beforeLoad: spies.guard,
    loader: spies.loader,
    component: lazyRouteComponent(spies.section),
  })
  const keys = createRoute({
    getParentRoute: () => layout,
    path: 'keys/',
    beforeLoad: spies.guard,
    loader: spies.loader,
  }).lazy(spies.keys as never)
  const router = createRouter({
    routeTree: root.addChildren([
      auth.addChildren([signIn]),
      layout.addChildren([dashboardIndex, dashboardSection, keys]),
    ]),
    history: createMemoryHistory({ initialEntries: ['/sign-in'] }),
  })
  return { router, spies }
}

afterEach(() => {
  sessionStorage.clear()
})

describe('prefetchRouteChunks', () => {
  test('downloads the chunks of every route of the page, without running a guard or loader', async () => {
    const { router, spies } = createTestRouter()
    await router.load()
    const navigations = vi.fn()
    router.subscribe('onBeforeNavigate', navigations)

    await prefetchRouteChunks(router, '/dashboard/overview')

    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.section).toHaveBeenCalledTimes(1)
    expect(spies.keys).not.toHaveBeenCalled()
    expect(spies.guard).not.toHaveBeenCalled()
    expect(spies.loader).not.toHaveBeenCalled()
    expect(navigations).not.toHaveBeenCalled()
    expect(router.state.location.href).toBe('/sign-in')
    expect(router.history.location.pathname).toBe('/sign-in')
    expect(router.state.matches.map((match) => match.routeId)).toEqual([
      '__root__',
      '/(auth)',
      '/(auth)/sign-in',
    ])
  })

  test('a section index route also downloads the section it redirects to', async () => {
    const { router, spies } = createTestRouter()

    await prefetchRouteChunks(router, '/dashboard')

    expect(spies.indexGuard).not.toHaveBeenCalled()
    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.section).toHaveBeenCalledTimes(1)
  })

  test('downloads the lazy file of a lazy route, whatever the query and hash', async () => {
    const { router, spies } = createTestRouter()

    await prefetchRouteChunks(router, '/keys?page=2#top')

    expect(spies.keys).toHaveBeenCalledTimes(1)
    expect(spies.layout).toHaveBeenCalledTimes(1)
    expect(spies.section).not.toHaveBeenCalled()
    expect(spies.guard).not.toHaveBeenCalled()
    expect(spies.loader).not.toHaveBeenCalled()
  })

  test('an href that matches no page downloads nothing', async () => {
    const { router, spies } = createTestRouter()

    await prefetchRouteChunks(router, '/no-such-page')

    expect(spies.layout).not.toHaveBeenCalled()
    expect(spies.section).not.toHaveBeenCalled()
    expect(spies.keys).not.toHaveBeenCalled()
  })

  test('a failed download stays silent, and the navigation loads the chunk again', async () => {
    const consoleError = vi.spyOn(console, 'error')
    const storageWrite = vi.spyOn(Storage.prototype, 'setItem')
    const { router, spies } = createTestRouter({
      section: vi
        .fn<() => Promise<Page>>()
        .mockRejectedValueOnce(chunkLoadError('42'))
        .mockImplementation(page),
      keys: () => Promise.reject(chunkLoadError('43')),
    })
    await router.load()

    await expect(
      prefetchRouteChunks(router, '/dashboard/overview')
    ).resolves.toBeUndefined()
    await expect(prefetchRouteChunks(router, '/keys')).resolves.toBeUndefined()

    expect(spies.section).toHaveBeenCalledTimes(1)
    expect(spies.keys).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
    // No reload: both reload-once guards write their marker first.
    expect(storageWrite).not.toHaveBeenCalled()
    expect(router.state.location.href).toBe('/sign-in')

    await router.navigate({ to: '/dashboard' })

    expect(router.state.location.href).toBe('/dashboard/overview')
    expect(spies.section).toHaveBeenCalledTimes(2)
    expect(router.state.matches.at(-1)).toMatchObject({
      routeId: '/_authenticated/dashboard/$section',
      status: 'success',
    })
  })
})
