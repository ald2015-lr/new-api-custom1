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
import { useQuery } from '@tanstack/react-query'

import { requireServerSuccess } from '@/lib/server-error-message'

import { getChannelQuotaLimits } from '../api'
import type { ChannelQuotaLimit, ChannelQuotaLimitsResponse } from '../types'

export const CHANNEL_QUOTA_LIMITS_QUERY_KEY = ['channel-quota-limits'] as const

// Rows mount their own observers after the channel list loads; keeping the
// data fresh briefly stops each new observer from refetching. The channels
// table invalidates it explicitly whenever the list itself refreshes.
const CHANNEL_QUOTA_LIMITS_STALE_TIME_MS = 30_000

type ChannelQuotaLimitMap = ReadonlyMap<number, ChannelQuotaLimit>

function mapQuotaLimitsByChannel(
  response: ChannelQuotaLimitsResponse
): ChannelQuotaLimitMap {
  const limits = new Map<number, ChannelQuotaLimit>()
  for (const item of response.data ?? []) {
    if (item && typeof item === 'object') {
      limits.set(item.channel_id, item)
    }
  }
  return limits
}

/**
 * All channel quota limits keyed by channel id. Every caller shares one
 * cached request, so table cells can read their own row cheaply.
 */
export function useChannelQuotaLimits() {
  return useQuery({
    queryKey: CHANNEL_QUOTA_LIMITS_QUERY_KEY,
    queryFn: async () => requireServerSuccess(await getChannelQuotaLimits()),
    select: mapQuotaLimitsByChannel,
    staleTime: CHANNEL_QUOTA_LIMITS_STALE_TIME_MS,
  })
}

/** Quota limit of one channel, or undefined when the channel has none. */
export function useChannelQuotaLimit(
  channelId: number
): ChannelQuotaLimit | undefined {
  const { data } = useChannelQuotaLimits()
  return data?.get(channelId)
}
