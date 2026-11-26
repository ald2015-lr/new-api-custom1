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
import { api } from '@/lib/api'
import { markServerErrorHandled } from '@/lib/handle-server-error'
import { getServerErrorStatus } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import type { PricingData } from './types'

// ----------------------------------------------------------------------------
// Pricing APIs
// ----------------------------------------------------------------------------

/**
 * Get model pricing data.
 *
 * Without a session, a 401 means the administrator requires sign-in for the
 * model square and a 403 that it is switched off. The page redirects for both
 * (usePricingAccessRedirect), so such an answer gets no session refresh,
 * which cannot succeed, and no "Session expired!" or other toast.
 */
export async function getPricing(): Promise<PricingData> {
  const anonymous = !useAuthStore.getState().auth.accessToken
  try {
    const res = await api.get(
      '/api/pricing',
      anonymous ? { skipAuthRefresh: true, skipErrorHandler: true } : undefined
    )
    return res.data
  } catch (error) {
    const status = getServerErrorStatus(error)
    if (anonymous && (status === 401 || status === 403)) {
      markServerErrorHandled(error)
    }
    throw error
  }
}
