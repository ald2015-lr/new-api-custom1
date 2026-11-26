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
import type { QueryClient } from '@tanstack/react-query'
import axios from 'axios'
import { t } from 'i18next'

import { publishAuthSessionEvent } from '@/lib/auth-session-sync'
import { retryAfterMs } from '@/lib/server-error-message'
import { hasSessionHint } from '@/lib/session-hint'
import {
  useAuthStore,
  type AuthBootstrapState,
  type AuthBundle,
  type AuthUser,
  type LoginSession,
} from '@/stores/auth-store'

export type RefreshOutcome =
  | { kind: 'authenticated'; bundle: AuthBundle }
  | { kind: 'anonymous' }
  | { kind: 'transient_error'; error: unknown; retryAfterMs?: number }
  | { kind: 'out_of_sync'; code?: string }

type TransientRefreshOutcome = Extract<
  RefreshOutcome,
  { kind: 'transient_error' }
>

export interface AuthRefreshHTTPResponse {
  status: number
  data?: unknown
  error?: unknown
  /** The response's `Retry-After` delay in milliseconds, when it sent one. */
  retryAfterMs?: number
}

export interface AuthRefreshRuntime {
  request: (expectedSID?: string) => Promise<AuthRefreshHTTPResponse>
  getExpectedSID: () => string | undefined
  parseBundle: (value: unknown) => AuthBundle | null
  acceptBundle: (bundle: AuthBundle) => void
  clear: (synchronizeTabs: boolean, bootstrapState?: AuthBootstrapState) => void
  markTransient: () => void
  wait: (delay: number) => Promise<void>
  isCurrent?: () => boolean
}

export interface AuthTokenRotation {
  access_token: string
  token_type: string
  access_expires_at: number
  session: LoginSession
}

export class AuthRotationError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'AuthRotationError'
  }
}

/**
 * Thrown by the protected-route guard when the server could not be asked about
 * the session (rate limit, server or network trouble). The Refresh Cookie may
 * still be valid, so this is a retry page, never a sign-out.
 */
export class AuthSessionUnavailableError extends Error {
  constructor(options?: ErrorOptions) {
    super('The login session cannot be restored right now', options)
    this.name = 'AuthSessionUnavailableError'
  }
}

export function isAuthSessionUnavailableError(
  error: unknown
): error is AuthSessionUnavailableError {
  return (
    error instanceof AuthSessionUnavailableError ||
    (error instanceof Error && error.name === 'AuthSessionUnavailableError')
  )
}

const authClient = axios.create({
  baseURL: '',
  withCredentials: true,
  timeout: 30000,
  headers: {
    // no-store forbids storage; no-cache also revalidates any older cached response.
    'Cache-Control': 'no-cache, no-store',
  },
})

const refreshRaceDelays = [80, 200, 500] as const
let refreshPromise: Promise<RefreshOutcome> | null = null
let authEpoch = 0

// After a temporary refresh failure, `resolveAuthentication` answers with that
// failure for a while instead of asking again: every guard of one navigation
// (and the sign-in page it may lead to) would otherwise spend a request of the
// same rate-limit budget. `Retry-After` sets the pause, within these bounds.
const TRANSIENT_REFRESH_MIN_PAUSE_MS = 5_000
const TRANSIENT_REFRESH_MAX_PAUSE_MS = 60_000
let lastTransientRefresh: TransientRefreshOutcome | null = null
let transientRefreshUntil = 0

function clearTransientRefresh(): void {
  lastTransientRefresh = null
  transientRefreshUntil = 0
}

function rememberTransientRefresh(outcome: TransientRefreshOutcome): void {
  const pause = Math.min(
    Math.max(outcome.retryAfterMs ?? 0, TRANSIENT_REFRESH_MIN_PAUSE_MS),
    TRANSIENT_REFRESH_MAX_PAUSE_MS
  )
  lastTransientRefresh = outcome
  transientRefreshUntil = Date.now() + pause
}

function currentTransientRefresh(): TransientRefreshOutcome | null {
  if (lastTransientRefresh && Date.now() < transientRefreshUntil) {
    return lastTransientRefresh
  }
  return null
}

class AuthRefreshSupersededError extends Error {
  constructor() {
    super('Authentication refresh was superseded')
    this.name = 'AuthRefreshSupersededError'
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object'
}

function isAuthUser(value: unknown): value is AuthUser {
  if (!isRecord(value)) return false
  return (
    Number.isInteger(value.id) &&
    Number(value.id) > 0 &&
    typeof value.username === 'string' &&
    typeof value.role === 'number'
  )
}

function isLoginSession(value: unknown): value is LoginSession {
  if (!isRecord(value)) return false
  return (
    typeof value.sid === 'string' &&
    value.sid.length > 0 &&
    typeof value.current === 'boolean' &&
    typeof value.login_method === 'string' &&
    typeof value.ip === 'string' &&
    typeof value.user_agent === 'string' &&
    typeof value.created_at === 'number' &&
    typeof value.last_active_at === 'number' &&
    typeof value.expires_at === 'number'
  )
}

function hasValidTokenFields(value: Record<string, unknown>): boolean {
  return (
    typeof value.access_token === 'string' &&
    value.access_token.length > 0 &&
    typeof value.token_type === 'string' &&
    value.token_type.length > 0 &&
    typeof value.access_expires_at === 'number' &&
    Number.isFinite(value.access_expires_at) &&
    value.access_expires_at > 0
  )
}

export function isAuthBundle(value: unknown): value is AuthBundle {
  if (!isRecord(value)) return false
  return (
    hasValidTokenFields(value) &&
    isAuthUser(value.user) &&
    isLoginSession(value.session)
  )
}

function isAuthTokenRotation(value: unknown): value is AuthTokenRotation {
  return (
    isRecord(value) &&
    hasValidTokenFields(value) &&
    value.token_type === 'Bearer' &&
    isLoginSession(value.session) &&
    value.session.current
  )
}

export function applyAuthBundle(
  bundle: AuthBundle,
  synchronizeTabs = true
): void {
  const previousSID = useAuthStore.getState().auth.session?.sid
  authEpoch += 1
  clearTransientRefresh()
  useAuthStore.getState().auth.setBundle(bundle)
  if (synchronizeTabs && previousSID !== bundle.session.sid) {
    publishAuthSessionEvent('authenticated', bundle.session.sid)
  }
}

export function applyAuthRotation(value: unknown): void {
  if (!isAuthTokenRotation(value)) {
    throw new AuthRotationError('Invalid authentication rotation response')
  }

  const auth = useAuthStore.getState().auth
  if (!auth.user || !auth.session) {
    throw new AuthRotationError('Authentication rotation has no active session')
  }
  if (value.session.sid !== auth.session.sid) {
    throw new AuthRotationError('Authentication rotation session mismatch')
  }

  applyAuthBundle(
    {
      access_token: value.access_token,
      token_type: value.token_type,
      access_expires_at: value.access_expires_at,
      session: value.session,
      user: auth.user,
    },
    false
  )
}

export function clearAuthentication(
  synchronizeTabs = true,
  bootstrapState: AuthBootstrapState = 'complete'
): void {
  const sid = useAuthStore.getState().auth.session?.sid
  authEpoch += 1
  clearTransientRefresh()
  useAuthStore.getState().auth.reset(bootstrapState)
  if (synchronizeTabs && sid) {
    publishAuthSessionEvent('signed_out', sid)
  }
}

export function clearAuthenticatedClientState(
  queryClient: QueryClient,
  synchronizeTabs = true
): void {
  queryClient.clear()
  clearAuthentication(synchronizeTabs)
}

function waitForRefreshRace(delay: number): Promise<void> {
  return new Promise((resolve) => globalThis.setTimeout(resolve, delay))
}

export function createRefreshRunner(
  runtime: AuthRefreshRuntime
): () => Promise<RefreshOutcome> {
  const superseded = (): RefreshOutcome => ({
    kind: 'transient_error',
    error: new AuthRefreshSupersededError(),
  })
  const run = async (
    raceAttempt: number,
    allowMismatchRetry: boolean
  ): Promise<RefreshOutcome> => {
    if (runtime.isCurrent && !runtime.isCurrent()) return superseded()
    const response = await runtime.request(runtime.getExpectedSID())
    if (runtime.isCurrent && !runtime.isCurrent()) return superseded()
    const responseData = isRecord(response.data) ? response.data : undefined
    const code =
      typeof responseData?.code === 'string' ? responseData.code : undefined
    const bundle = runtime.parseBundle(responseData?.data)
    if (responseData?.success === true && bundle) {
      runtime.acceptBundle(bundle)
      return { kind: 'authenticated', bundle }
    }

    if (response.status === 409 && code === 'AUTH_REFRESH_RACE') {
      const delay = refreshRaceDelays[raceAttempt]
      if (delay !== undefined) {
        await runtime.wait(delay)
        return run(raceAttempt + 1, allowMismatchRetry)
      }
      runtime.clear(false)
      return { kind: 'out_of_sync', code }
    }

    if (response.status === 409 && code === 'AUTH_SESSION_MISMATCH') {
      if (allowMismatchRetry) {
        runtime.clear(false, 'idle')
        return run(0, false)
      }
      runtime.clear(false)
      return { kind: 'out_of_sync', code }
    }

    if (response.status === 401) {
      runtime.clear(true)
      return { kind: 'anonymous' }
    }

    if (!response.status || response.status >= 500 || response.status === 429) {
      runtime.markTransient()
      return {
        kind: 'transient_error',
        error: response.error ?? response.data,
        retryAfterMs: response.retryAfterMs,
      }
    }

    runtime.clear(false)
    return {
      kind: 'out_of_sync',
      code: code ?? 'AUTH_INVALID_REFRESH_RESPONSE',
    }
  }

  return () => run(0, true)
}

async function requestRefresh(
  expectedSID?: string
): Promise<AuthRefreshHTTPResponse> {
  try {
    const response = await authClient.post(
      '/api/user/auth/refresh',
      undefined,
      {
        headers: expectedSID ? { 'X-Auth-Session': expectedSID } : undefined,
      }
    )
    return { status: response.status, data: response.data }
  } catch (error: unknown) {
    if (!axios.isAxiosError(error)) return { status: 0, error }
    return {
      status: error.response?.status ?? 0,
      data: error.response?.data,
      error,
      retryAfterMs: retryAfterMs(error),
    }
  }
}

function runRefresh(refreshEpoch: number): Promise<RefreshOutcome> {
  return createRefreshRunner({
    request: requestRefresh,
    getExpectedSID: () => useAuthStore.getState().auth.session?.sid,
    parseBundle: (value) => (isAuthBundle(value) ? value : null),
    acceptBundle: (bundle) => applyAuthBundle(bundle, false),
    clear: (synchronizeTabs, bootstrapState) => {
      if (!synchronizeTabs && bootstrapState === 'idle') {
        useAuthStore.getState().auth.reset('idle')
        return
      }
      clearAuthentication(synchronizeTabs, bootstrapState)
    },
    markTransient: () => useAuthStore.getState().auth.setBootstrapState('idle'),
    wait: waitForRefreshRace,
    isCurrent: () => authEpoch === refreshEpoch,
  })()
}

async function performRefreshWithBrowserLock(
  refreshEpoch: number
): Promise<RefreshOutcome> {
  try {
    if (typeof navigator === 'undefined' || !navigator.locks) {
      return runRefresh(refreshEpoch)
    }
    return navigator.locks.request(
      'new-api:auth-refresh',
      { mode: 'exclusive' },
      () => runRefresh(refreshEpoch)
    )
  } catch (error: unknown) {
    useAuthStore.getState().auth.setBootstrapState('idle')
    return { kind: 'transient_error', error }
  }
}

export function refreshAuthentication(): Promise<RefreshOutcome> {
  if (!refreshPromise) {
    const refreshEpoch = authEpoch
    refreshPromise = performRefreshWithBrowserLock(refreshEpoch)
      .then((outcome) => {
        // A newer sign-in or sign-out supersedes this answer; only a failure
        // of the server's own answer pauses further attempts.
        if (
          outcome.kind === 'transient_error' &&
          authEpoch === refreshEpoch &&
          !(outcome.error instanceof AuthRefreshSupersededError)
        ) {
          rememberTransientRefresh(outcome)
        }
        return outcome
      })
      .finally(() => {
        refreshPromise = null
      })
  }
  return refreshPromise
}

function currentValidAuthBundle(): AuthBundle | null {
  const auth = useAuthStore.getState().auth
  if (
    !auth.user ||
    !auth.accessToken ||
    !auth.accessExpiresAt ||
    !auth.session ||
    auth.accessExpiresAt <= Math.floor(Date.now() / 1000)
  ) {
    return null
  }
  return {
    access_token: auth.accessToken,
    token_type: 'Bearer',
    access_expires_at: auth.accessExpiresAt,
    user: auth.user,
    session: auth.session,
  }
}

/**
 * Resolve authentication from memory, or from the server when memory is empty.
 *
 * Use this wherever the answer decides what the user sees: route guards that
 * redirect on the result, and the sign-in page. It contacts the server on a
 * cold cache even when no session hint is present, so a usable Refresh Cookie
 * is always honoured.
 *
 * Shortly after a temporary failure (see `rememberTransientRefresh`) it returns
 * that failure without a request; `retryAuthentication` asks again at once.
 */
export async function resolveAuthentication(): Promise<RefreshOutcome> {
  const bundle = currentValidAuthBundle()
  if (bundle) {
    useAuthStore.getState().auth.setBootstrapState('complete')
    return { kind: 'authenticated', bundle }
  }

  const auth = useAuthStore.getState().auth
  const hasStaleSession = Boolean(auth.user && auth.session)
  if (auth.bootstrapState === 'complete' && !hasStaleSession) {
    return { kind: 'anonymous' }
  }

  const transient = currentTransientRefresh()
  if (transient) return transient

  auth.setBootstrapState('checking')
  return refreshAuthentication()
}

/**
 * Resolve authentication again now, without the pause that follows a
 * temporary failure. For an explicit user action such as a Retry button.
 */
export function retryAuthentication(): Promise<RefreshOutcome> {
  clearTransientRefresh()
  return resolveAuthentication()
}

/**
 * Resolve authentication on the public boot path, skipping a refresh that the
 * server's session hint says would fail.
 *
 * The skip leaves `bootstrapState` at `idle` rather than `complete`: a missing
 * hint is not a server verdict, so it must not be recorded as a finished
 * anonymous check. `resolveAuthentication` therefore still reaches the network
 * later, which is what lets a hintless visitor holding a valid Refresh Cookie
 * recover the moment authentication actually matters.
 */
export async function bootstrapAuthentication(): Promise<RefreshOutcome> {
  if (!currentValidAuthBundle() && !hasSessionHint()) {
    const auth = useAuthStore.getState().auth
    if (!auth.user && !auth.session) {
      return { kind: 'anonymous' }
    }
  }
  return resolveAuthentication()
}

export function getCommonHeaders(): Record<string, string> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }
  const accessToken = useAuthStore.getState().auth.accessToken
  if (accessToken) {
    headers.Authorization = `Bearer ${accessToken}`
  }
  return headers
}

export async function getFreshAuthHeaders(): Promise<Record<string, string>> {
  const auth = useAuthStore.getState().auth
  const refreshBefore = Math.floor(Date.now() / 1000) + 60
  if (
    auth.accessToken &&
    auth.accessExpiresAt &&
    auth.accessExpiresAt > refreshBefore
  ) {
    return getCommonHeaders()
  }

  const outcome = await refreshAuthentication()
  if (outcome.kind === 'authenticated') {
    return getCommonHeaders()
  }

  const current = useAuthStore.getState().auth
  if (
    current.accessToken &&
    current.accessExpiresAt &&
    current.accessExpiresAt > Math.floor(Date.now() / 1000)
  ) {
    return getCommonHeaders()
  }

  if (outcome.kind === 'transient_error') {
    throw new Error(t('Request failed'), { cause: outcome.error })
  }
  throw new Error(t('Session expired!'))
}
