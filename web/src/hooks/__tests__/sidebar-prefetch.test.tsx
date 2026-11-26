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
import { renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { expect, test, vi } from 'vitest'

import type { NavLink } from '@/components/layout/types'
import { prefetchDashboardSection } from '@/features/dashboard/lib/section-chunks'

import { useSidebarData } from '../use-sidebar-data'

vi.mock('@/features/dashboard/lib/section-chunks', () => ({
  prefetchDashboardSection: vi.fn(),
}))

test('the sidebar Dashboard link prefetches the model charts it opens', () => {
  const client = new QueryClient()
  client.setQueryData(['status'], {})
  const { result } = renderHook(() => useSidebarData(), {
    wrapper: (props: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        {props.children}
      </QueryClientProvider>
    ),
  })
  const dashboard = result.current.navGroups
    .flatMap((group) => group.items)
    .find((item) => item.url === '/dashboard/models') as NavLink | undefined

  dashboard?.prefetch?.()

  expect(prefetchDashboardSection).toHaveBeenCalledWith('models')
})
