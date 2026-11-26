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
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { DashboardSectionId } from '../section-registry'

// The dashboard sections other than the overview render their charts from
// lazy chunks nested in the dashboard route (the chart library alone is about
// 560 KB). Every section shares the route of the overview, so the router's
// preload on link hover finds nothing to load; the section components are
// loaded through these functions instead, by their React.lazy wrappers and
// ahead of a visit by prefetchDashboardSection.
export const loadLogStatCards = () =>
  import('../components/models/log-stat-cards')
export const loadPerformanceOverview = () =>
  import('../components/models/performance-overview')
export const loadConsumptionDistributionChart = () =>
  import('../components/models/consumption-distribution-chart')
export const loadModelCharts = () => import('../components/models/model-charts')
export const loadFlowCharts = () => import('../components/flow/flow-charts')
export const loadUserCharts = () => import('../components/users/user-charts')

type ChunkLoader = () => Promise<unknown>

const SECTION_CHUNKS: Partial<
  Record<DashboardSectionId, { all: ChunkLoader[]; admin: ChunkLoader[] }>
> = {
  models: {
    all: [loadLogStatCards, loadConsumptionDistributionChart, loadModelCharts],
    admin: [loadPerformanceOverview],
  },
  flow: { all: [loadFlowCharts], admin: [] },
  users: { all: [], admin: [loadUserCharts] },
}

/**
 * Start downloading the chart chunks of a dashboard section, for example when
 * the pointer reaches a link to it. Sections without lazy charts, chunks the
 * current user would not see and failed downloads are ignored (the section
 * loads them again when it renders).
 */
export function prefetchDashboardSection(section: string): void {
  const chunks = SECTION_CHUNKS[section as DashboardSectionId]
  if (!chunks) return
  const role = useAuthStore.getState().auth.user?.role ?? 0
  const loaders =
    role >= ROLE.ADMIN ? [...chunks.all, ...chunks.admin] : chunks.all
  for (const load of loaders) load().catch(() => undefined)
}

type IdleWindow = Window & {
  requestIdleCallback?: (
    callback: () => void,
    options?: { timeout: number }
  ) => number
  cancelIdleCallback?: (handle: number) => void
}

type NetworkInformationLike = { saveData?: boolean; effectiveType?: string }

function prefersLessData(): boolean {
  const connection = (
    navigator as Navigator & { connection?: NetworkInformationLike }
  ).connection
  return (
    connection?.saveData === true ||
    connection?.effectiveType === 'slow-2g' ||
    connection?.effectiveType === '2g'
  )
}

/** Delay before an idle prefetch, so the page's own requests go first. */
export const IDLE_PREFETCH_DELAY_MS = 2000

/**
 * Prefetch a section's charts once the page has settled: after
 * IDLE_PREFETCH_DELAY_MS, at the browser's next idle period. Skipped when the
 * browser asks to save data or reports a 2G connection. Returns a function
 * that cancels the prefetch if it has not started yet.
 */
export function prefetchDashboardSectionWhenIdle(section: string): () => void {
  if (typeof window === 'undefined' || prefersLessData()) return () => undefined

  const idleWindow = window as IdleWindow
  let idleHandle: number | undefined
  const timer = window.setTimeout(() => {
    const run = () => prefetchDashboardSection(section)
    if (idleWindow.requestIdleCallback) {
      idleHandle = idleWindow.requestIdleCallback(run, { timeout: 5000 })
    } else {
      run()
    }
  }, IDLE_PREFETCH_DELAY_MS)

  return () => {
    window.clearTimeout(timer)
    if (idleHandle !== undefined) idleWindow.cancelIdleCallback?.(idleHandle)
  }
}
