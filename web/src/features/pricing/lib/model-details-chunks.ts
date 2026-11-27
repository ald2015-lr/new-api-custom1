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

// The model details (the drawer of the model square and the
// /pricing/$modelId page) are a chunk of their own, so the model list does
// not download them. Its performance charts and the highlighter of the code
// samples are separate chunks again, loaded when their tab opens (see
// model-details-lazy.tsx).
export const loadModelDetails = () => import('../components/model-details')

let detailsPrefetch: Promise<unknown> | null = null

/**
 * Start downloading the model details ahead of opening them, for example when
 * the pointer reaches a model card. Cheap to call on every pointer move: only
 * the first call (or the first after a failure) starts a download. Failures
 * are otherwise ignored: the drawer loads the chunk again when it opens.
 */
export function prefetchModelDetails(): void {
  detailsPrefetch ??= loadModelDetails().catch(() => {
    detailsPrefetch = null
  })
}

/**
 * Prefetch the model details once the model square has settled. Returns a
 * function that cancels the prefetch if it has not started yet.
 */
export function prefetchModelDetailsWhenIdle(): () => void {
  return prefetchWhenIdle(prefetchModelDetails)
}
