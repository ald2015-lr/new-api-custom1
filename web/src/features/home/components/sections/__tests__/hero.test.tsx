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
import { render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { STATUS_QUERY_KEY } from '@/lib/status-query'

import { Hero } from '../hero'

vi.mock('../../hero-terminal-demo', () => ({ HeroTerminalDemo: () => null }))

let client: QueryClient

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

afterEach(() => {
  client.clear()
})

async function renderHero(options: {
  isAuthenticated: boolean
  headerNavModules?: Record<string, unknown>
}) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(STATUS_QUERY_KEY, {
    docs_link: 'https://docs.example.com',
    HeaderNavModules: options.headerNavModules
      ? JSON.stringify(options.headerNavModules)
      : '',
  })
  const root = createRootRoute({
    component: () => (
      <>
        <Hero isAuthenticated={options.isAuthenticated} />
        <Outlet />
      </>
    ),
  })
  const pages = ['/', '/pricing', '/dashboard', '/sign-up'].map((path) =>
    createRoute({ getParentRoute: () => root, path, component: () => null })
  )
  const router = createRouter({
    routeTree: root.addChildren(pages),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

function heroActions() {
  const dashboard = screen.queryByRole('button', { name: /Go to Dashboard/ })
  const getStarted = screen.queryByRole('button', { name: /Get Started/ })
  const primary = dashboard ?? getStarted
  if (!primary?.parentElement) throw new Error('hero actions not found')
  return primary.parentElement
}

describe('home hero actions', () => {
  it('signed-in visitors get Dashboard, Model Square and Docs', async () => {
    await renderHero({ isAuthenticated: true })

    const actions = heroActions()
    const links = within(actions).getAllByRole('button')
    expect(links.map((link) => link.textContent?.trim())).toEqual([
      'Go to Dashboard',
      'Model Square',
      'Docs',
    ])
    expect(
      within(actions).getByRole('button', { name: 'Model Square' })
    ).toHaveAttribute('href', '/pricing')
  })

  it('the primary action takes the full row on phones', async () => {
    await renderHero({ isAuthenticated: true })

    const actions = heroActions()
    expect(actions).toHaveClass('grid', 'grid-cols-2', 'sm:flex')
    expect(
      within(actions).getByRole('button', { name: /Go to Dashboard/ })
    ).toHaveClass('col-span-2')
  })

  it('hides the pricing entries when the model square is disabled', async () => {
    await renderHero({
      isAuthenticated: true,
      headerNavModules: { pricing: { enabled: false, requireAuth: false } },
    })

    const actions = heroActions()
    expect(
      within(actions).queryByRole('button', { name: 'Model Square' })
    ).toBeNull()
    expect(within(actions).getByRole('button', { name: 'Docs' })).toHaveClass(
      'col-span-2'
    )
  })

  it('anonymous visitors keep Get Started, View Pricing and Docs', async () => {
    await renderHero({ isAuthenticated: false })

    const links = within(heroActions()).getAllByRole('button')
    expect(links.map((link) => link.textContent?.trim())).toEqual([
      'Get Started',
      'View Pricing',
      'Docs',
    ])
  })
})
