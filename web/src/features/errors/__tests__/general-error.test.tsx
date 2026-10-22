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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { GeneralError } from '../general-error'

const CHUNK_URL = 'http://localhost/static/js/async/42.js'

/** Shape thrown by rspack's JSONP chunk loader in production builds. */
function rspackChunkLoadError(): Error {
  const error = new Error(`Loading chunk 42 failed.\n(error: ${CHUNK_URL})`)
  error.name = 'ChunkLoadError'
  Object.assign(error, { request: CHUNK_URL, type: 'error' })
  return error
}

/** Shape thrown by rspack's CSS chunk loader. */
function rspackCssChunkLoadError(): Error {
  return Object.assign(
    new Error('Loading CSS chunk 42 failed.\n(/static/css/42.old.css)'),
    { code: 'CSS_CHUNK_LOAD_FAILED' }
  )
}

const chunkErrors = [
  { kind: 'JS', createError: rspackChunkLoadError },
  { kind: 'CSS', createError: rspackCssChunkLoadError },
]

const reload = vi.fn()

beforeEach(() => {
  sessionStorage.clear()
  vi.stubGlobal('location', { ...window.location, reload })
})

afterEach(() => {
  vi.unstubAllGlobals()
  sessionStorage.clear()
  reload.mockReset()
})

function createTestRouter(error: unknown) {
  const root = createRootRoute({
    component: Outlet,
    errorComponent: GeneralError,
  })
  const home = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () => <p>Current page</p>,
  })
  const broken = createRoute({
    getParentRoute: () => root,
    path: '/wallet',
    component: lazyRouteComponent(() => Promise.reject(error)),
  })
  return createRouter({
    routeTree: root.addChildren([home, broken]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
    defaultPreload: 'intent',
  })
}

/** Renders the app on a working page, then navigates to the broken route. */
async function openBrokenRoute(error: unknown) {
  const router = createTestRouter(error)
  const view = render(<RouterProvider router={router} />)
  await screen.findByText('Current page')
  await act(() => router.navigate({ to: '/wallet' }))
  return view
}

/** Fails the route once (which reloads), then fails it again after the reload. */
async function openPersistentlyBrokenRoute(error: unknown) {
  const beforeReload = await openBrokenRoute(error)
  await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
  beforeReload.unmount()
  return openBrokenRoute(error)
}

test.each(chunkErrors)(
  'a failed $kind route chunk reloads the page once and renders nothing while it reloads',
  async ({ createError }) => {
    const view = await openBrokenRoute(createError())

    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    expect(view.container).toBeEmptyDOMElement()
  }
)

test.each(chunkErrors)(
  'a $kind route chunk that still fails after its reload shows the chunk notice without a numeric heading',
  async ({ createError }) => {
    await openPersistentlyBrokenRoute(createError())

    expect(
      await screen.findByText('Failed to load part of the page')
    ).toBeVisible()
    expect(
      screen.getByText(
        'A new version may have been deployed. Please reload the page.',
        { exact: false }
      )
    ).toBeVisible()
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
    expect(reload).toHaveBeenCalledTimes(1)
  }
)

test('the Reload page button on the chunk notice reloads the page', async () => {
  await openPersistentlyBrokenRoute(rspackChunkLoadError())
  reload.mockClear()

  await userEvent.click(
    await screen.findByRole('button', { name: 'Reload page' })
  )

  expect(reload).toHaveBeenCalledTimes(1)
})

test('failed and repeated intent preloads keep the current page usable until navigation', async () => {
  const router = createTestRouter(rspackChunkLoadError())
  render(<RouterProvider router={router} />)
  await screen.findByText('Current page')

  await act(() => router.preloadRoute({ to: '/wallet' }))
  await act(() => router.preloadRoute({ to: '/wallet' }))

  expect(screen.getByText('Current page')).toBeVisible()
  expect(reload).not.toHaveBeenCalled()
  expect(
    screen.queryByText('Failed to load part of the page')
  ).not.toBeInTheDocument()
  await act(() => router.navigate({ to: '/wallet' }))
  await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
})

test('a chunk failure with blocked session storage shows the chunk notice without risking a reload loop', async () => {
  vi.stubGlobal('sessionStorage', {
    getItem: () => null,
    setItem: () => {
      throw new Error('Storage blocked')
    },
    clear: () => {},
  })

  await openBrokenRoute(rspackChunkLoadError())

  expect(
    await screen.findByText('Failed to load part of the page')
  ).toBeVisible()
  expect(reload).not.toHaveBeenCalled()
})

test('a 429 response shows the Too many requests notice under its 429 heading without reloading', async () => {
  await openBrokenRoute({ response: { status: 429 } })

  expect(await screen.findByRole('heading', { name: '429' })).toBeVisible()
  expect(screen.getByText('Too many requests')).toBeVisible()
  expect(
    screen.getByText(/Please wait a moment before trying again/)
  ).toBeVisible()
  expect(reload).not.toHaveBeenCalled()
})

test('a 500 response shows the generic notice under its 500 heading without reloading', async () => {
  await openBrokenRoute({ response: { status: 500 } })

  expect(await screen.findByRole('heading', { name: '500' })).toBeVisible()
  expect(screen.getByText(/Oops! Something went wrong/)).toBeVisible()
  expect(reload).not.toHaveBeenCalled()
})

test('an error without an HTTP status shows the generic notice and a Reload page button without a fake numeric heading', async () => {
  await openBrokenRoute(new Error('Render failed'))

  expect(await screen.findByText(/Oops! Something went wrong/)).toBeVisible()
  expect(screen.getByRole('button', { name: 'Reload page' })).toBeVisible()
  expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
  expect(reload).not.toHaveBeenCalled()
})
