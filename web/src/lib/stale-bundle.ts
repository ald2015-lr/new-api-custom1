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
import type { AxiosResponse } from 'axios'
import { t } from 'i18next'
import { toast } from 'sonner'

import { getClientBuildVersion } from '@/lib/build-metadata'

/**
 * Stale-bundle detection.
 *
 * Every response from the Go server carries `X-New-Api-Version`. The client
 * bundle is stamped with the same value at build time
 * (`VITE_REACT_APP_VERSION`). When the two disagree the server was redeployed
 * after this tab loaded, and the running bundle may reference chunks that no
 * longer exist.
 */

const SERVER_VERSION_HEADER = 'x-new-api-version'
const PLACEHOLDER_VERSION = 'v0.0.0'
const NOTICE_STORAGE_KEY_PREFIX = 'newapi:version-notice:'
const NOTICE_TOAST_ID = 'newapi-stale-bundle'

// In-memory guard so that a tab whose sessionStorage throws (private mode,
// blocked site data) still sees the notice at most once per server version.
const notifiedVersions = new Set<string>()

// The first mismatching response can arrive before the root route has mounted
// the <Toaster>; a toast raised then is silently dropped. Hold the version
// until the app reports that the toaster is ready.
let toasterReady = false
let pendingServerVersion: string | null = null

export function isStaleBundle(
  serverVersion: string | null | undefined,
  clientVersion: string | null | undefined
): boolean {
  if (!serverVersion || !clientVersion) return false
  if (
    serverVersion === PLACEHOLDER_VERSION ||
    clientVersion === PLACEHOLDER_VERSION
  ) {
    return false
  }
  return serverVersion !== clientVersion
}

/**
 * Show the "new version available" toast once per server version per tab.
 */
export function notifyStaleBundleOnce(serverVersion: string): void {
  if (typeof window === 'undefined' || notifiedVersions.has(serverVersion)) {
    return
  }
  if (!toasterReady) {
    pendingServerVersion = serverVersion
    return
  }
  notifiedVersions.add(serverVersion)

  const key = NOTICE_STORAGE_KEY_PREFIX + serverVersion
  try {
    if (window.sessionStorage.getItem(key)) return
    window.sessionStorage.setItem(key, String(Date.now()))
  } catch {
    // Storage unavailable; the in-memory guard above still prevents repeats.
  }

  toast(t('A new version is available. Reload to update.'), {
    id: NOTICE_TOAST_ID,
    duration: Number.POSITIVE_INFINITY,
    action: {
      label: t('Reload'),
      onClick: () => window.location.reload(),
    },
  })
}

/**
 * Called once the <Toaster> is mounted. Releases a notice that was raised
 * before the app could display it.
 */
export function flushPendingStaleBundleNotice(): void {
  toasterReady = true
  const pending = pendingServerVersion
  pendingServerVersion = null
  if (pending) notifyStaleBundleOnce(pending)
}

/**
 * Compare a response's server version header with the running bundle and
 * raise the stale-bundle notice when they differ. Safe to call for every
 * response; responses without the header are ignored.
 */
export function checkServerVersionHeader(
  headers: AxiosResponse['headers'] | undefined
): void {
  const serverVersion = headers?.[SERVER_VERSION_HEADER]
  if (typeof serverVersion !== 'string') return
  if (!isStaleBundle(serverVersion, getClientBuildVersion())) return
  notifyStaleBundleOnce(serverVersion)
}
