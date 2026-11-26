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
import { useNavigate, useRouter } from '@tanstack/react-router'
import { useEffect } from 'react'

import { getServerErrorStatus } from '@/lib/server-error-message'

/**
 * Leave the model square when /api/pricing refuses the visitor: a 401 means
 * the administrator requires sign-in for it (sign-in, then back here), a 403
 * that it is switched off (home page). The route guard makes the same choice
 * from /api/status; this covers a guard that decided from an outdated status
 * snapshot or the defaults. Returns whether such a redirect is under way.
 */
export function usePricingAccessRedirect(error: unknown): boolean {
  const navigate = useNavigate()
  const router = useRouter()
  const status = getServerErrorStatus(error)
  const refused = status === 401 || status === 403

  useEffect(() => {
    if (status === 401) {
      void navigate({
        to: '/sign-in',
        search: { redirect: router.state.location.href },
        replace: true,
      })
    } else if (status === 403) {
      void navigate({ to: '/', replace: true })
    }
  }, [navigate, router, status])

  return refused
}
