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
import { useRouter } from '@tanstack/react-router'
import { useEffect } from 'react'

import {
  DEFAULT_AUTH_REDIRECT,
  sanitizeAuthRedirect,
} from '@/features/auth/lib/auth-redirect'
import { prefetchWhenIdle } from '@/lib/idle-prefetch'
import { prefetchRouteChunks } from '@/lib/route-chunk-prefetch'

/**
 * Download the code of the page a sign-in lands on (the safe `redirectTo`,
 * else the dashboard) once this page has settled, so that the step into the
 * console after a sign-in does not wait for it: the console's shared code is
 * not part of the first load. Only code is fetched, no data, and the sign-in
 * itself is left alone (see prefetchRouteChunks). Skipped when the browser
 * asks to save data or reports a 2G connection, and dropped when the page is
 * left before it starts (see prefetchWhenIdle).
 */
export function usePostLoginPrefetch(redirectTo?: string): void {
  const router = useRouter()
  useEffect(() => {
    const target =
      sanitizeAuthRedirect(redirectTo, window.location.origin) ??
      DEFAULT_AUTH_REDIRECT
    return prefetchWhenIdle(() => {
      void prefetchRouteChunks(router, target)
    })
  }, [router, redirectTo])
}
