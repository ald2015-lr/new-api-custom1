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
import { useAuthStore } from '@/stores/auth-store'

function refusalTarget(
  status: number | undefined,
  signedIn: boolean
): 'sign-in' | 'home' | null {
  if (status === 403) return 'home'
  if (status === 401 && !signedIn) return 'sign-in'
  return null
}

/**
 * Leave the model square when /api/pricing refuses the visitor: a 401 means
 * the administrator requires sign-in for it (sign-in, then back here), a 403
 * that it is switched off (home page). The route guard makes the same choice
 * from /api/status; this covers a guard that decided from an outdated status
 * snapshot or the defaults. Returns whether such a redirect is under way.
 *
 * A signed-in visitor's 401 is not that requirement: it is what is left when
 * the session could not be refreshed for a moment (a session that really
 * ended already sends the visitor to sign in, see lib/http-client.ts). The
 * sign-in page would send such a visitor straight back, so the page keeps
 * what it shows and offers a retry instead.
 */
export function usePricingAccessRedirect(error: unknown): boolean {
  const navigate = useNavigate()
  const router = useRouter()
  const signedIn = useAuthStore((state) => Boolean(state.auth.user))
  const target = refusalTarget(getServerErrorStatus(error), signedIn)

  useEffect(() => {
    if (target === 'sign-in') {
      void navigate({
        to: '/sign-in',
        search: { redirect: router.state.location.href },
        replace: true,
      })
    } else if (target === 'home') {
      void navigate({ to: '/', replace: true })
    }
  }, [navigate, router, target])

  return target !== null
}
