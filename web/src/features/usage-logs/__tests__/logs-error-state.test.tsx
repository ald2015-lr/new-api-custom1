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
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { UsageLogsProvider } from '../components/usage-logs-provider'
import { UsageLogsTable } from '../components/usage-logs-table'
import { usageLogSchema } from '../data/schema'

type LogsReply = () => Promise<unknown>

const okLogs: LogsReply = async () => ({
  data: {
    success: true,
    data: {
      items: [
        usageLogSchema.parse({
          id: 1,
          user_id: 1,
          created_at: 1788840000,
          type: 2,
          content: '',
          quota: 5000,
          model_name: 'claude-sonnet-4-5',
          other: '{}',
        }),
      ],
      total: 1,
    },
  },
})

function renderLogs(replies: LogsReply[]) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'tester',
    role: ROLE.USER,
  })
  const logRequests: string[] = []
  vi.spyOn(api, 'get').mockImplementation(async (url: string) => {
    if (url.startsWith('/api/log/self?')) {
      logRequests.push(url)
      const reply = replies[Math.min(logRequests.length, replies.length) - 1]
      return reply()
    }
    return { data: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: () => <UsageLogsTable logCategory='common' />,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: ['/usage-logs/common'] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UsageLogsProvider>
        <RouterProvider router={router} />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
  return { logRequests }
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
})

describe('usage logs load failure', () => {
  test('a rejected logs query shows the failure and a Retry button, not "No Logs Found"', async () => {
    // Database pool exhaustion arrives as HTTP 200 with success: false.
    renderLogs([
      async () => ({
        data: { success: false, message: 'Failed to query logs' },
      }),
    ])

    expect(await screen.findByText('Failed to load logs')).toBeVisible()
    expect(screen.getByText('Failed to query logs')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeVisible()
    expect(screen.queryByText('No Logs Found')).not.toBeInTheDocument()
  })

  test('Retry loads the logs again', async () => {
    const { logRequests } = renderLogs([
      async () => {
        throw Object.assign(new Error('Network Error'), {
          isAxiosError: true,
          code: 'ERR_NETWORK',
        })
      },
      okLogs,
    ])
    await screen.findByText('Failed to load logs')

    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findAllByText('$0.01')).not.toHaveLength(0)
    expect(screen.queryByText('Failed to load logs')).not.toBeInTheDocument()
    expect(logRequests).toHaveLength(2)
  })

  test('an empty result still says that no logs exist', async () => {
    renderLogs([
      async () => ({ data: { success: true, data: { items: [], total: 0 } } }),
    ])

    expect(await screen.findByText('No Logs Found')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Retry' })
    ).not.toBeInTheDocument()
  })
})
