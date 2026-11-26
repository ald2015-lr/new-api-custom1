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
import { prefetchWhenIdle } from '@/lib/idle-prefetch'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import type { DashboardSectionId } from '../section-registry'

export { IDLE_PREFETCH_DELAY_MS } from '@/lib/idle-prefetch'

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

/**
 * Prefetch a section's charts once the page has settled (see
 * prefetchWhenIdle). Returns a function that cancels the prefetch if it has
 * not started yet.
 */
export function prefetchDashboardSectionWhenIdle(section: string): () => void {
  return prefetchWhenIdle(() => prefetchDashboardSection(section))
}
