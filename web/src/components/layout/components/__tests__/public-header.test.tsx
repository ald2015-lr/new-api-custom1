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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SYSTEM_UPDATE_QUERY_KEY } from '@/features/system-update/use-system-update'
import { api } from '@/lib/api'
import { DEFAULT_SYSTEM_NAME } from '@/lib/constants'
import { ROLE } from '@/lib/roles'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

import { PublicHeader } from '../public-header'

// The desktop nav and the mobile overlay render Model Square links too, and
// jsdom applies no breakpoints, so the mobile shortcut is located by test id.
const MOBILE_PRICING_TEST_ID = 'public-header-mobile-pricing'

let client: QueryClient

beforeEach(() => {
  // jsdom has no scrolling; router scroll restoration would log a warning.
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/notice') return { data: { success: true, data: '' } }
    throw new Error(`Unexpected GET ${url}`)
  })
})

afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  document.body.style.overflow = ''
})

async function renderHeader(options: {
  path?: string
  headerNavModules?: Record<string, unknown>
}) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(STATUS_QUERY_KEY, {
    HeaderNavModules: options.headerNavModules
      ? JSON.stringify(options.headerNavModules)
      : '',
  })
  // A fresh update check keeps the admin version button from calling GitHub.
  const checkedAt = Date.now()
  client.setQueryData(SYSTEM_UPDATE_QUERY_KEY, {
    release: null,
    lastCheckedAt: checkedAt,
    lastAttemptAt: checkedAt,
    error: null,
  })
  const root = createRootRoute({
    component: () => (
      <>
        <PublicHeader />
        <Outlet />
      </>
    ),
  })
  const pages = ['/', '/pricing', '/pricing/$modelId', '/sign-in'].map((path) =>
    createRoute({
      getParentRoute: () => root,
      path,
      component: () => <main>{path}</main>,
    })
  )
  const router = createRouter({
    routeTree: root.addChildren(pages),
    history: createMemoryHistory({ initialEntries: [options.path ?? '/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return router
}

describe('mobile Model Square shortcut', () => {
  it('default header modules show a Model Square link to /pricing', async () => {
    await renderHeader({})

    const link = await screen.findByTestId(MOBILE_PRICING_TEST_ID)

    expect(link).toBeVisible()
    expect(link).toHaveAccessibleName('Model Square')
    expect(link).toHaveAttribute('href', '/pricing')
  })

  it('on a narrow screen the shortcut, not the brand, shrinks and ellipsizes when the row runs out of room', async () => {
    await renderHeader({})

    const link = await screen.findByTestId(MOBILE_PRICING_TEST_ID)
    const mobileActions = screen.getByRole('button', {
      name: 'Toggle navigation menu',
    }).parentElement

    // jsdom has no layout, so the overflow strategy is checked by its classes.
    expect(mobileActions).toHaveClass('min-w-0', 'gap-1', 'min-[360px]:gap-2')
    expect(mobileActions).toHaveClass('lg:hidden')
    expect(mobileActions).not.toHaveClass('shrink-0')
    expect(link).toHaveClass('min-w-0', 'shrink', 'max-w-25')
    expect(link).not.toHaveClass('shrink-0')
    expect(within(link).getByText('Model Square')).toHaveClass('truncate')
  })

  it.each([
    ['anonymous visitor', null, 'min-w-7', '@min-[4.5rem]/system-brand:block'],
    ['admin', ROLE.ADMIN, 'min-w-16', '@min-[7rem]/system-brand:block'],
  ])(
    'for an %s the brand keeps room for the logo and any version button and hides a squeezed site name',
    async (_label, role, brandMinWidth, nameShownFrom) => {
      if (role !== null) {
        useAuthStore.getState().auth.setUser({ id: 1, username: 'u', role })
      }
      await renderHeader({})

      const siteName = await screen.findByTitle(DEFAULT_SYSTEM_NAME)
      const homeLink = siteName.closest('a')

      expect(homeLink).toHaveClass('min-w-7')
      expect(homeLink?.parentElement).toHaveClass(brandMinWidth)
      expect(siteName).toHaveClass('hidden', nameShownFrom)
    }
  )

  it('a disabled pricing module renders no shortcut', async () => {
    await renderHeader({
      headerNavModules: { pricing: { enabled: false, requireAuth: false } },
    })

    await screen.findByRole('button', { name: 'Toggle navigation menu' })

    expect(screen.queryByTestId(MOBILE_PRICING_TEST_ID)).toBeNull()
  })

  it('anonymous click on a login-required pricing module opens the sign-in prompt and stays on /', async () => {
    const user = userEvent.setup()
    const router = await renderHeader({
      headerNavModules: { pricing: { enabled: true, requireAuth: true } },
    })

    await user.click(await screen.findByTestId(MOBILE_PRICING_TEST_ID))

    expect(
      await screen.findByRole('dialog', { name: 'Sign in required' })
    ).toBeVisible()
    expect(router.state.location.pathname).toBe('/')
  })

  it('keyboard: Tab reaches the login-required shortcut as a link and Enter opens the sign-in prompt without leaving /', async () => {
    const user = userEvent.setup()
    const router = await renderHeader({
      headerNavModules: { pricing: { enabled: true, requireAuth: true } },
    })
    const link = await screen.findByTestId(MOBILE_PRICING_TEST_ID)

    // jsdom applies no breakpoints, so the desktop controls come first.
    for (let i = 0; i < 30 && document.activeElement !== link; i++) {
      await user.tab()
    }
    expect(link).toHaveFocus()
    expect(link).toHaveRole('link')

    await user.keyboard('{Enter}')

    expect(
      await screen.findByRole('dialog', { name: 'Sign in required' })
    ).toBeVisible()
    expect(router.state.location.pathname).toBe('/')
  })

  it('clicking the shortcut with the menu open navigates to /pricing and closes the menu', async () => {
    const user = userEvent.setup()
    const router = await renderHeader({})
    await user.click(
      await screen.findByRole('button', { name: 'Toggle navigation menu' })
    )
    expect(document.body.style.overflow).toBe('hidden')

    await user.click(screen.getByTestId(MOBILE_PRICING_TEST_ID))

    await waitFor(() => expect(router.state.location.pathname).toBe('/pricing'))
    expect(document.body.style.overflow).toBe('')
  })

  it.each([
    ['/pricing', 'page'],
    ['/pricing/gpt-4o', 'page'],
    ['/', null],
  ])('on %s the shortcut has aria-current=%s', async (path, expected) => {
    await renderHeader({ path })

    const link = await screen.findByTestId(MOBILE_PRICING_TEST_ID)

    expect(link.getAttribute('aria-current')).toBe(expected)
  })
})
