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
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  ClientIpDiagnostics,
  ConfirmPaymentComplianceResponse,
  FetchUpstreamRatiosRequest,
  InviteAbusePreviewParams,
  InviteAbusePreviewResponse,
  LogCleanupTask,
  SystemOptionsResponse,
  SystemTaskListResponse,
  SystemTaskFilters,
  SystemTaskResponse,
  UpdateOptionRequest,
  UpdateOptionResponse,
  UpdatePasskeyDomainsRequest,
  UpdatePasskeyDomainsResponse,
  UpstreamChannelsResponse,
  UpstreamRatiosResponse,
} from './types'

export async function getSystemOptions() {
  const res = await api.get<SystemOptionsResponse>('/api/option/')
  return res.data
}

export async function updateSystemOption(request: UpdateOptionRequest) {
  const res = await api.put<UpdateOptionResponse>('/api/option/', request)
  return res.data
}

/**
 * Save the invite abuse rule's changed options in one request; the server
 * saves all of them or none.
 */
export async function updateInviteAbuseOptions(
  options: Record<string, UpdateOptionRequest['value']>
) {
  const res = await api.put<UpdateOptionResponse>('/api/option/invite_abuse', {
    options,
  })
  return res.data
}

export async function updatePasskeyDomains(
  request: UpdatePasskeyDomainsRequest
) {
  const res = await api.put<UpdatePasskeyDomainsResponse>(
    '/api/option/passkey/domains',
    request,
    {
      validateStatus: (status) =>
        (status >= 200 && status < 300) || status === 409,
    }
  )
  return res.data
}

/**
 * The address the server resolves for the current admin, used to check the
 * reverse proxy setup before turning on the registration IP limit.
 */
export async function getClientIpDiagnostics(): Promise<ClientIpDiagnostics> {
  const res = await api.get('/api/option/client_ip')
  const body = requireServerSuccess(res.data) as Record<string, unknown>
  // Read the usual { success, data } envelope and a bare object alike.
  const payload =
    body.data && typeof body.data === 'object'
      ? (body.data as Record<string, unknown>)
      : body
  return {
    ip: typeof payload.ip === 'string' ? payload.ip : '',
    trusted_proxies_configured: payload.trusted_proxies_configured === true,
    // Older servers leave it out.
    trusted_proxies_catch_all: payload.trusted_proxies_catch_all === true,
  }
}

/** Count who the invite abuse rule would disable with these unsaved values. */
export async function previewInviteAbuseBan(params: InviteAbusePreviewParams) {
  const res = await api.get<InviteAbusePreviewResponse>(
    '/api/user/invite_abuse/preview',
    { params, disableDuplicate: true }
  )
  return res.data
}

export async function confirmPaymentCompliance() {
  const res = await api.post<ConfirmPaymentComplianceResponse>(
    '/api/option/payment_compliance',
    { confirmed: true }
  )
  return res.data
}

export async function startLogCleanupTask(targetTimestamp: number) {
  const res = await api.post<SystemTaskResponse<LogCleanupTask>>(
    '/api/system-task/log-cleanup',
    null,
    {
      params: { target_timestamp: targetTimestamp },
    }
  )
  return res.data
}

export async function getCurrentLogCleanupTask() {
  const res = await api.get<SystemTaskResponse<LogCleanupTask | null>>(
    '/api/system-task/current',
    {
      params: { type: 'log_cleanup' },
    }
  )
  return res.data
}

export async function getSystemTask(taskId: string) {
  const res = await api.get<SystemTaskResponse<LogCleanupTask>>(
    `/api/system-task/${taskId}`
  )
  return res.data
}

export async function listSystemTasks(
  limit = 20,
  filters: SystemTaskFilters = {}
) {
  const res = await api.get<SystemTaskListResponse>('/api/system-task/list', {
    params: { limit, ...filters },
  })
  return res.data
}

export async function resetModelRatios() {
  const res = await api.post<UpdateOptionResponse>(
    '/api/option/rest_model_ratio'
  )
  return res.data
}

export async function getUpstreamChannels() {
  const res = await api.get<UpstreamChannelsResponse>(
    '/api/ratio_sync/channels'
  )
  return res.data
}

export async function fetchUpstreamRatios(request: FetchUpstreamRatiosRequest) {
  const res = await api.post<UpstreamRatiosResponse>(
    '/api/ratio_sync/fetch',
    request
  )
  return res.data
}
