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
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { expect, test, vi } from 'vitest'

import { SidebarProvider } from '@/components/ui/sidebar'

import type { NavItem } from '../../types'
import { NavGroup } from '../nav-group'

async function renderSidebarLinks(items: NavItem[]) {
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <SidebarProvider>
          <NavGroup title='General' items={items} />
        </SidebarProvider>
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
}

test('a sidebar link runs its prefetch when the pointer reaches it', async () => {
  const prefetch = vi.fn()
  await renderSidebarLinks([
    { title: 'Dashboard', url: '/dashboard/models', prefetch },
  ])

  fireEvent.pointerEnter(await screen.findByRole('link', { name: 'Dashboard' }))

  expect(prefetch).toHaveBeenCalled()
})

test('a sidebar link runs its prefetch when keyboard focus reaches it', async () => {
  const prefetch = vi.fn()
  await renderSidebarLinks([
    { title: 'Dashboard', url: '/dashboard/models', prefetch },
  ])
  const link = await screen.findByRole('link', { name: 'Dashboard' })

  act(() => link.focus())

  expect(prefetch).toHaveBeenCalled()
})

test('sidebar links without a prefetch keep working', async () => {
  await renderSidebarLinks([{ title: 'API Keys', url: '/keys' }])
  const link = await screen.findByRole('link', { name: 'API Keys' })

  fireEvent.pointerEnter(link)
  act(() => link.focus())

  expect(link).toHaveAttribute('href', '/keys')
})
