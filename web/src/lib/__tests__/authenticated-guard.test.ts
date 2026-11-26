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
import { isRedirect } from '@tanstack/react-router'
import { afterEach, describe, expect, test, vi } from 'vitest'

import {
  AuthSessionUnavailableError,
  resolveAuthentication,
  type RefreshOutcome,
} from '@/lib/auth-session'
import { Route } from '@/routes/_authenticated/route'
import { useAuthStore, type AuthBundle } from '@/stores/auth-store'

vi.mock('@/lib/auth-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/auth-session')>()),
  resolveAuthentication: vi.fn(),
}))

const bundle: AuthBundle = {
  access_token: 'access-token',
  token_type: 'Bearer',
  access_expires_at: Math.floor(Date.now() / 1000) + 600,
  user: { id: 42, username: 'test-user', role: 1 },
  session: {
    sid: 'session-a',
    current: true,
    login_method: 'password',
    ip: '127.0.0.1',
    user_agent: 'test',
    created_at: 100,
    last_active_at: 100,
    expires_at: 1000,
  },
}

const beforeLoad = Route.options.beforeLoad as unknown as (context: {
  location: { href: string }
}) => Promise<void>

/** Run the guard for a hard load of `href` and return what it threw. */
async function guard(outcome: RefreshOutcome, href = '/console/log?p=2') {
  vi.mocked(resolveAuthentication).mockResolvedValueOnce(outcome)
  try {
    await beforeLoad({ location: { href } })
  } catch (error) {
    return error
  }
  return undefined
}

/** The cookie the server sets next to the refresh cookie at sign-in. */
function setSessionHint() {
  document.cookie = 'new_api_has_session=1; path=/'
}

afterEach(() => {
  useAuthStore.getState().auth.reset('idle')
  document.cookie =
    'new_api_has_session=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/'
})

describe('protected route guard', () => {
  test('a temporary refresh failure shows the retry page instead of signing out', async () => {
    setSessionHint()
    const cause = new Error('Request failed with status code 429')

    const thrown = await guard({ kind: 'transient_error', error: cause })

    expect(thrown).toBeInstanceOf(AuthSessionUnavailableError)
    expect(isRedirect(thrown)).toBe(false)
    expect((thrown as Error).cause).toBe(cause)
  })

  test.each([
    ['rate limited', 'Request failed with status code 429'],
    ['server error', 'Request failed with status code 503'],
    ['network error', 'Network Error'],
  ])(
    'a visitor without a session hint goes to sign-in when the refresh fails (%s)',
    async (_, message) => {
      // No session hint: most likely never signed in, so the retry page
      // would claim a sign-in that does not exist.
      const thrown = await guard({
        kind: 'transient_error',
        error: new Error(message),
      })

      expect(thrown).not.toBeInstanceOf(AuthSessionUnavailableError)
      expect(isRedirect(thrown)).toBe(true)
      expect((thrown as { options: unknown }).options).toMatchObject({
        to: '/sign-in',
        search: { redirect: '/console/log?p=2' },
      })
    }
  )

  test('a confirmed anonymous visitor goes to sign-in with the page to return to', async () => {
    const thrown = await guard({ kind: 'anonymous' })

    expect(isRedirect(thrown)).toBe(true)
    expect((thrown as { options: unknown }).options).toMatchObject({
      to: '/sign-in',
      search: { redirect: '/console/log?p=2' },
    })
  })

  test('an out-of-sync session goes to sign-in as before', async () => {
    const thrown = await guard({
      kind: 'out_of_sync',
      code: 'AUTH_REFRESH_RACE',
    })

    expect(isRedirect(thrown)).toBe(true)
  })

  test('a signed-in user passes, even when a refresh of an expired token failed', async () => {
    useAuthStore.getState().auth.setBundle(bundle)

    expect(await guard({ kind: 'authenticated', bundle })).toBeUndefined()
    expect(
      await guard({ kind: 'transient_error', error: new Error('offline') })
    ).toBeUndefined()
  })
})
