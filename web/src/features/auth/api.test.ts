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
import { generateKeyPairSync, webcrypto } from 'node:crypto'

import { act, renderHook } from '@testing-library/react'
import type { AxiosAdapter } from 'axios'
import { toast } from 'sonner'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api, type RefreshOutcome } from '@/lib/api'
import type { AuthBundle } from '@/stores/auth-store'

import {
  executeLogout,
  getRegisterCaptcha,
  login,
  register,
  sendEmailVerification,
  sendPasswordResetEmail,
} from './api'
import { useOAuthLogin } from './hooks/use-oauth-login'
import { consumeOAuthLoginRedirect } from './lib/oauth-callback-mode'
import { clearPasswordEncryptionCache } from './lib/password-encryption'

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test.each([true, false])(
  'starts Telegram OAuth only when configuration is ready: %s',
  async (configured) => {
    vi.spyOn(window, 'localStorage', 'get').mockReturnValue(
      window.sessionStorage
    )
    const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
      if (url === '/api/oauth/state') {
        return {
          data: {
            success: true,
            data: {
              flow_token: 'telegram-state',
              authorization_url: 'https://oauth.telegram.org/auth?server=pkce',
            },
          },
        }
      }
      if (url === '/api/user/auth/logout') return { data: { success: true } }
      throw new Error(`Unexpected POST ${url}`)
    })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const error = vi.spyOn(toast, 'error')
    const { result } = renderHook(() =>
      useOAuthLogin(
        { telegram_oauth: true, telegram_oauth_configured: configured },
        '/console/personal'
      )
    )
    await act(() => result.current.handleTelegramLogin())
    if (configured) {
      expect(post.mock.calls.map(([url]) => url)).toEqual([
        '/api/oauth/state',
        '/api/user/auth/logout',
      ])
      expect(post).toHaveBeenCalledWith(
        '/api/oauth/state',
        expect.objectContaining({ provider: 'telegram', intent: 'login' }),
        expect.anything()
      )
      expect(open).toHaveBeenCalledWith(
        'https://oauth.telegram.org/auth?server=pkce',
        '_self'
      )
      expect(consumeOAuthLoginRedirect('telegram-state')).toBe(
        '/console/personal'
      )
    } else {
      expect(post).not.toHaveBeenCalled()
      expect(open).not.toHaveBeenCalled()
      expect(error).toHaveBeenCalledWith(
        'Telegram OAuth is not configured or enabled. Please contact your administrator.'
      )
    }
  }
)

const bundle: AuthBundle = {
  access_token: 'access-token',
  token_type: 'Bearer',
  access_expires_at: 1_900_000_000,
  user: { id: 1, username: 'test-user', role: 1 },
  session: {
    sid: 'session-b',
    current: true,
    login_method: 'password',
    ip: '127.0.0.1',
    user_agent: 'test',
    created_at: 1,
    last_active_at: 1,
    expires_at: 1_900_000_000,
  },
}

function mismatchError() {
  return {
    isAxiosError: true,
    response: {
      status: 409,
      data: { code: 'AUTH_SESSION_MISMATCH' },
    },
  }
}

describe('logout coordination', () => {
  test('returns an unsuccessful response without pretending to sign out', async () => {
    let refreshCount = 0
    const result = await executeLogout({
      getExpectedSID: () => 'session-a',
      request: async () => ({ success: false, message: 'not revoked' }),
      refresh: async () => {
        refreshCount += 1
        return { kind: 'anonymous' }
      },
    })

    expect(result).toEqual({ success: false, message: 'not revoked' })
    expect(refreshCount).toBe(0)
  })

  test('recovers a cookie mismatch and retries with the refreshed SID', async () => {
    let sid = 'session-a'
    const requestedSIDs: Array<string | undefined> = []
    const result = await executeLogout({
      getExpectedSID: () => sid,
      request: async (expectedSID) => {
        requestedSIDs.push(expectedSID)
        if (requestedSIDs.length === 1) throw mismatchError()
        return { success: true, message: '' }
      },
      refresh: async () => {
        sid = bundle.session.sid
        return { kind: 'authenticated', bundle }
      },
    })

    expect(result).toEqual({ success: true, message: '' })
    expect(requestedSIDs).toEqual(['session-a', 'session-b'])
  })

  test('treats a mismatch that refresh confirms anonymous as signed out', async () => {
    const result = await executeLogout({
      getExpectedSID: () => 'session-a',
      request: async () => {
        throw mismatchError()
      },
      refresh: async () => ({ kind: 'anonymous' }),
    })

    expect(result).toEqual({ success: true, message: '' })
  })

  test('preserves the active session when mismatch recovery is temporary', async () => {
    const originalError = mismatchError()
    const transient: RefreshOutcome = {
      kind: 'transient_error',
      error: new Error('offline'),
    }

    await expect(
      executeLogout({
        getExpectedSID: () => 'session-a',
        request: async () => {
          throw originalError
        },
        refresh: async () => transient,
      })
    ).rejects.toBe(originalError)
  })
})

describe('registration requests', () => {
  const account = {
    username: 'new-user',
    password: 'password123',
    aff_code: 'INVITE',
  }

  test('sends Turnstile and the captcha as query parameters, not in the body', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, message: '' } })

    await register({
      ...account,
      turnstile: 'turnstile-token',
      captcha_id: 'captcha-id',
      captcha: '12',
    })

    expect(post).toHaveBeenCalledWith(
      '/api/user/register',
      { ...account, turnstile: 'turnstile-token' },
      {
        params: {
          turnstile: 'turnstile-token',
          captcha_id: 'captcha-id',
          captcha: '12',
        },
      }
    )
  })

  test('registers without captcha parameters when none was answered', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, message: '' } })

    await register(account)

    const config = post.mock.calls[0][2]
    expect(config?.params).toStrictEqual({
      turnstile: '',
      captcha_id: undefined,
      captcha: undefined,
    })
  })

  test('sends the email code request with Turnstile and the captcha', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, message: '' } })

    await sendEmailVerification('123456789@qq.com', 'turnstile-token', {
      captcha_id: 'captcha-id',
      captcha: '7',
    })

    expect(get).toHaveBeenCalledWith('/api/verification', {
      params: {
        email: '123456789@qq.com',
        turnstile: 'turnstile-token',
        captcha_id: 'captcha-id',
        captcha: '7',
      },
    })
  })

  test('fetches a separate captcha for each concurrent refresh', async () => {
    let issued = 0
    const adapter = vi.fn<AxiosAdapter>(async (config) => {
      issued += 1
      return {
        data: {
          success: true,
          data: {
            captcha_id: `captcha-${issued}`,
            image: 'data:image/png;base64,AA==',
            expires_in: 300,
          },
        },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      }
    })
    const originalAdapter = api.defaults.adapter
    api.defaults.adapter = adapter
    try {
      const [first, second] = await Promise.all([
        getRegisterCaptcha(),
        getRegisterCaptcha(),
      ])

      expect(adapter).toHaveBeenCalledTimes(2)
      expect(adapter.mock.calls[0][0].url).toBe('/api/captcha')
      expect([first.data?.captcha_id, second.data?.captcha_id]).toEqual([
        'captcha-1',
        'captcha-2',
      ])
    } finally {
      api.defaults.adapter = originalAdapter
    }
  })
})

describe('password login and reset requests', () => {
  const captcha = { captcha_id: 'captcha-id', captcha: '12' }

  test('signs in with Turnstile and the captcha as query parameters and the password in the body', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: false, message: '' } })

    await login({
      username: 'alice',
      password: 'password123',
      turnstile: 'turnstile-token',
      captcha,
    })

    expect(post).toHaveBeenCalledWith(
      '/api/user/login',
      { username: 'alice', password: 'password123' },
      {
        params: {
          turnstile: 'turnstile-token',
          captcha_id: 'captcha-id',
          captcha: '12',
        },
        skipAuthRefresh: true,
      }
    )
  })

  test('signs in with an encrypted password and keeps the captcha out of the body', async () => {
    vi.stubGlobal('crypto', webcrypto)
    clearPasswordEncryptionCache()
    const { publicKey } = generateKeyPairSync('rsa', {
      modulusLength: 2048,
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          kid: 'login-key',
          public_key: publicKey
            .export({ type: 'spki', format: 'pem' })
            .toString(),
        },
      },
    })
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, message: '' } })

    await login({
      username: 'alice',
      password: 'password123',
      passwordEncryptionEnabled: true,
      captcha,
    })

    const [url, body, config] = post.mock.calls[0]
    expect(url).toBe('/api/user/login')
    expect(Object.keys(body as object).sort()).toEqual([
      'encryption_key_id',
      'password_encrypted',
      'username',
    ])
    expect(body).toMatchObject({
      username: 'alice',
      encryption_key_id: 'login-key',
    })
    expect(JSON.stringify(body)).not.toContain('password123')
    expect(config).toEqual({
      params: { turnstile: '', captcha_id: 'captcha-id', captcha: '12' },
      skipAuthRefresh: true,
    })
    clearPasswordEncryptionCache()
  })

  test('signs in without captcha parameters while the captcha is off', async () => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: false, message: '' } })

    await login({ username: 'alice', password: 'password123' })

    expect(post.mock.calls[0][2]?.params).toStrictEqual({
      turnstile: '',
      captcha_id: undefined,
      captcha: undefined,
    })
  })

  test('requests a reset email with Turnstile and the captcha', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, message: '' } })

    await sendPasswordResetEmail('user@example.com', 'turnstile-token', captcha)

    expect(get).toHaveBeenCalledWith('/api/reset_password', {
      params: {
        email: 'user@example.com',
        turnstile: 'turnstile-token',
        captcha_id: 'captcha-id',
        captcha: '12',
      },
    })
  })

  test('requests a reset email without captcha parameters while the captcha is off', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, message: '' } })

    await sendPasswordResetEmail('user@example.com')

    expect(get.mock.calls[0][1]?.params).toStrictEqual({
      email: 'user@example.com',
      turnstile: undefined,
      captcha_id: undefined,
      captcha: undefined,
    })
  })
})
