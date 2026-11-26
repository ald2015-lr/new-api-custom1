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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { ROLE } from '@/lib/roles'

// The section components stand in for their chunks: each records when it is
// loaded. Every test starts from a fresh module registry.
const SECTION_MODULES = {
  'log-stat-cards': ['../../components/models/log-stat-cards', 'LogStatCards'],
  'performance-overview': [
    '../../components/models/performance-overview',
    'PerformanceOverview',
  ],
  'consumption-distribution-chart': [
    '../../components/models/consumption-distribution-chart',
    'ConsumptionDistributionChart',
  ],
  'model-charts': ['../../components/models/model-charts', 'ModelCharts'],
  'flow-charts': ['../../components/flow/flow-charts', 'FlowCharts'],
  'user-charts': ['../../components/users/user-charts', 'UserCharts'],
} as const

let loaded: string[] = []

async function setUp(role: number) {
  const { useAuthStore } = await import('@/stores/auth-store')
  useAuthStore.getState().auth.setUser({ id: 1, username: 'user', role })
  return import('../section-chunks')
}

async function loadedEventually(expected: string[]) {
  await vi.waitFor(() => expect([...loaded].sort()).toEqual(expected.sort()))
}

beforeEach(() => {
  vi.resetModules()
  loaded = []
  for (const [name, [path, exportName]] of Object.entries(SECTION_MODULES)) {
    vi.doMock(path, () => {
      loaded.push(name)
      return { [exportName]: () => null }
    })
  }
})

afterEach(() => {
  for (const [path] of Object.values(SECTION_MODULES)) vi.doUnmock(path)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('prefetchDashboardSection', () => {
  test('the model charts of a user load the stat cards and both charts', async () => {
    const chunks = await setUp(ROLE.USER)

    chunks.prefetchDashboardSection('models')

    await loadedEventually([
      'log-stat-cards',
      'consumption-distribution-chart',
      'model-charts',
    ])
  })

  test('the model charts of an admin also load the performance overview', async () => {
    const chunks = await setUp(ROLE.ADMIN)

    chunks.prefetchDashboardSection('models')

    await loadedEventually([
      'log-stat-cards',
      'performance-overview',
      'consumption-distribution-chart',
      'model-charts',
    ])
  })

  test('the user analytics load only for admins, the flow view for everyone', async () => {
    const chunks = await setUp(ROLE.USER)

    chunks.prefetchDashboardSection('users')
    chunks.prefetchDashboardSection('flow')

    await loadedEventually(['flow-charts'])
  })

  test('the overview and unknown sections load nothing', async () => {
    const chunks = await setUp(ROLE.ADMIN)

    chunks.prefetchDashboardSection('overview')
    chunks.prefetchDashboardSection('no-such-section')
    await Promise.resolve()

    expect(loaded).toEqual([])
  })
})

describe('prefetchDashboardSectionWhenIdle', () => {
  test('starts after the settle delay at the next idle period', async () => {
    let idleCallback: (() => void) | undefined
    vi.stubGlobal('requestIdleCallback', (callback: () => void) => {
      idleCallback = callback
      return 1
    })
    const chunks = await setUp(ROLE.USER)
    vi.useFakeTimers()

    chunks.prefetchDashboardSectionWhenIdle('models')
    vi.advanceTimersByTime(chunks.IDLE_PREFETCH_DELAY_MS - 1)
    expect(idleCallback).toBeUndefined()
    vi.advanceTimersByTime(1)
    expect(loaded).toEqual([])

    vi.useRealTimers()
    idleCallback?.()
    await loadedEventually([
      'log-stat-cards',
      'consumption-distribution-chart',
      'model-charts',
    ])
  })

  test('cancelling before the delay ends loads nothing', async () => {
    const chunks = await setUp(ROLE.USER)
    vi.useFakeTimers()

    const cancel = chunks.prefetchDashboardSectionWhenIdle('models')
    cancel()
    vi.advanceTimersByTime(chunks.IDLE_PREFETCH_DELAY_MS * 10)
    await Promise.resolve()

    expect(loaded).toEqual([])
  })

  test('a browser that asks to save data does not prefetch', async () => {
    vi.stubGlobal(
      'navigator',
      Object.assign(Object.create(navigator), {
        connection: { saveData: true },
      })
    )
    const chunks = await setUp(ROLE.USER)
    vi.useFakeTimers()

    chunks.prefetchDashboardSectionWhenIdle('models')
    vi.advanceTimersByTime(chunks.IDLE_PREFETCH_DELAY_MS * 10)
    await Promise.resolve()

    expect(loaded).toEqual([])
  })
})
