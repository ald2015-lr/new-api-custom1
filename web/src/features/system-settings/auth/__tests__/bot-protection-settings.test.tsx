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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance, type i18n } from 'i18next'
import { useState } from 'react'
import { I18nextProvider } from 'react-i18next'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { UpdateOptionRequest } from '../../types'
import { BotProtectionSection } from '../bot-protection-section'

const defaults = {
  TurnstileCheckEnabled: false,
  TurnstileSiteKey: '',
  TurnstileSecretKey: '',
  RegisterCaptchaEnabled: true,
  RegisterIPRateLimitEnabled: false,
  RegisterIPRateLimitNum: 5,
  RegisterIPRateLimitMinutes: 15,
  PasswordResetCaptchaEnabled: true,
  PasswordResetEmailLimitEnabled: true,
  PasswordResetEmailCooldownSeconds: 120,
  PasswordResetEmailDailyMax: 5,
  LoginCaptchaEnabled: true,
  LoginLockoutEnabled: true,
  LoginLockoutMaxFailures: 10,
  LoginLockoutMinutes: 15,
}

let sectionDefaults: typeof defaults = defaults

function mockClientIp(body: Record<string, unknown>) {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/client_ip') return { data: body }
    throw new Error(`Unexpected GET ${url}`)
  })
}

let testI18n: i18n

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  )

  return (
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <div ref={setContainer} />
        <SettingsPageProvider actionsContainer={container}>
          <BotProtectionSection defaultValues={sectionDefaults} />
        </SettingsPageProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
}

async function renderSettings(values: Partial<typeof defaults> = {}) {
  sectionDefaults = { ...defaults, ...values }
  const route = createRootRoute({ component: Fixture })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
}

beforeEach(async () => {
  testI18n = createInstance()
  await testI18n.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} }, zh },
    // As in the app: keys are English sentences that may contain colons.
    nsSeparator: false,
    interpolation: { escapeValue: false },
  })
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
  mockClientIp({
    success: true,
    data: { ip: '203.0.113.7', trusted_proxies_configured: false },
  })
})

describe('Bot Protection settings saves', () => {
  it('saves both keys before enabling Turnstile in one save', async () => {
    const savedOptions: Record<string, string | number | boolean> = {}
    const put = vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
      const request = body as UpdateOptionRequest
      savedOptions[request.key] = request.value
      return { data: { success: true, message: '' } } as never
    })
    const user = userEvent.setup()
    await renderSettings()
    await user.type(
      screen.getByRole('textbox', { name: 'Site Key' }),
      'site-key'
    )
    await user.type(screen.getByLabelText('Secret Key'), 'secret-key')
    await user.click(screen.getByRole('switch', { name: 'Enable Turnstile' }))
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(put).toHaveBeenCalledTimes(3))
    expect(
      put.mock.calls.map(([, body]) => (body as UpdateOptionRequest).key)
    ).toEqual([
      'TurnstileSiteKey',
      'TurnstileSecretKey',
      'TurnstileCheckEnabled',
    ])
    expect(savedOptions).toEqual({
      TurnstileSiteKey: 'site-key',
      TurnstileSecretKey: 'secret-key',
      TurnstileCheckEnabled: true,
    })
  })
})

describe('Registration protection settings', () => {
  function savedKeys(put: { mock: { calls: unknown[][] } }) {
    return put.mock.calls.map((call) => call[1] as UpdateOptionRequest)
  }

  it('saves turning the registration captcha off and drops the cached sign-up status', async () => {
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    localStorage.setItem('status', JSON.stringify({ register_captcha: true }))
    await renderSettings()

    await user.click(
      screen.getByRole('switch', { name: 'Registration captcha' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    expect(savedKeys(put)).toEqual([
      { key: 'RegisterCaptchaEnabled', value: false },
    ])
    await waitFor(() => expect(localStorage.getItem('status')).toBeNull())
  })

  it('saves the IP limit count and window before turning the limit on', async () => {
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    await renderSettings()

    await user.click(
      screen.getByRole('switch', { name: 'Registration IP rate limit' })
    )
    const count = screen.getByRole('spinbutton', { name: 'Requests per IP' })
    await user.clear(count)
    await user.type(count, '10')
    const timeWindow = screen.getByRole('spinbutton', {
      name: 'Time window (minutes)',
    })
    await user.clear(timeWindow)
    await user.type(timeWindow, '30')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(put).toHaveBeenCalledTimes(3))
    expect(savedKeys(put)).toEqual([
      { key: 'RegisterIPRateLimitNum', value: 10 },
      { key: 'RegisterIPRateLimitMinutes', value: 30 },
      { key: 'RegisterIPRateLimitEnabled', value: true },
    ])
  })

  it.each([
    [
      'Requests per IP',
      '0',
      'Registration rate limit count must be an integer between 1 and 1000',
    ],
    [
      'Time window (minutes)',
      '1441',
      'Registration rate limit window must be an integer between 1 and 1440 minutes',
    ],
  ])('refuses %s set to %s and saves nothing', async (name, value, message) => {
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    await renderSettings()
    const input = screen.getByRole('spinbutton', { name })

    await user.clear(input)
    await user.type(input, value)
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  })

  it.each([
    [
      'enveloped',
      {
        success: true,
        data: { ip: '203.0.113.7', trusted_proxies_configured: false },
      },
      'TRUSTED_PROXIES is not configured, so only loopback and private network proxies are trusted.',
    ],
    [
      'bare',
      { ip: '203.0.113.7', trusted_proxies_configured: true },
      'TRUSTED_PROXIES is configured.',
    ],
  ])(
    'shows the IP address the server sees from a %s reply',
    async (_shape, body, proxies) => {
      const get = mockClientIp(body)
      await renderSettings()

      expect(await screen.findByText('203.0.113.7')).toBeInTheDocument()
      expect(screen.getByText(proxies)).toBeInTheDocument()
      expect(
        screen.getByText(
          'TRUSTED_PROXIES replaces the built-in loopback and private network defaults. List only the addresses of your own proxies, never 0.0.0.0/0 or ::/0.'
        )
      ).toBeInTheDocument()
      // A server that does not report a catch-all list shows no warning.
      expect(
        screen.queryByText('TRUSTED_PROXIES trusts every address')
      ).not.toBeInTheDocument()
      expect(get).toHaveBeenCalledWith('/api/option/client_ip')
    }
  )

  it('warns that any client can forge its address when TRUSTED_PROXIES trusts every address', async () => {
    mockClientIp({
      success: true,
      data: {
        ip: '203.0.113.7',
        trusted_proxies_configured: true,
        trusted_proxies_catch_all: true,
      },
    })
    await renderSettings()

    const warning = (
      await screen.findByText('TRUSTED_PROXIES trusts every address')
    ).closest('[data-slot=alert]')
    expect(warning).toHaveTextContent(
      'Any client can forge its IP address with X-Forwarded-For, so the registration IP limit and the login rate limit can be bypassed. List only the addresses of your own proxies.'
    )
  })

  it('describes both IP limit inputs of the row, the window with its captcha image limit', async () => {
    await renderSettings()

    expect(
      screen.getByRole('spinbutton', { name: 'Requests per IP' })
    ).toHaveAccessibleDescription(
      'Allowed in each time window, for registrations and for email codes alike'
    )
    expect(
      screen.getByRole('spinbutton', { name: 'Time window (minutes)' })
    ).toHaveAccessibleDescription(
      "Captcha images are limited per IP too. While this limit is on: six times the requests per IP in this window (at least 30), plus three for each request the server's critical rate limit allows while the login or password reset captcha is on. While it is off and any captcha is on: three images per such request (60 per 20 minutes by default)."
    )
  })
})

describe('Password reset and login protection settings', () => {
  function savedKeys(put: { mock: { calls: unknown[][] } }) {
    return put.mock.calls.map((call) => call[1] as UpdateOptionRequest)
  }

  it('shows the captchas, the reset email limit and the login lockout with their saved values', async () => {
    await renderSettings()

    for (const name of [
      'Password reset captcha',
      'Password reset email limit',
      'Login captcha',
      'Login lockout',
    ]) {
      expect(screen.getByRole('switch', { name })).toBeChecked()
    }
    expect(
      screen.getByRole('spinbutton', { name: 'Cooldown (seconds)' })
    ).toHaveValue(120)
    expect(
      screen.getByRole('spinbutton', { name: 'Emails per address in 24 hours' })
    ).toHaveValue(5)
    expect(
      screen.getByRole('spinbutton', { name: 'Failed attempts before locking' })
    ).toHaveValue(10)
    expect(
      screen.getByRole('spinbutton', { name: 'Lock duration (minutes)' })
    ).toHaveValue(15)
  })

  it.each([
    [
      'Cooldown (seconds)',
      '29',
      'Password reset email cooldown must be an integer between 30 and 3600 seconds',
    ],
    [
      'Cooldown (seconds)',
      '3601',
      'Password reset email cooldown must be an integer between 30 and 3600 seconds',
    ],
    [
      'Emails per address in 24 hours',
      '0',
      'Password reset email limit must be an integer between 1 and 100 per day',
    ],
    [
      'Emails per address in 24 hours',
      '101',
      'Password reset email limit must be an integer between 1 and 100 per day',
    ],
    [
      'Failed attempts before locking',
      '2',
      'Login lockout failure count must be an integer between 3 and 100',
    ],
    [
      'Failed attempts before locking',
      '101',
      'Login lockout failure count must be an integer between 3 and 100',
    ],
    [
      'Lock duration (minutes)',
      '0',
      'Login lockout duration must be an integer between 1 and 1440 minutes',
    ],
    [
      'Lock duration (minutes)',
      '1441',
      'Login lockout duration must be an integer between 1 and 1440 minutes',
    ],
  ])('refuses %s set to %s and saves nothing', async (name, value, message) => {
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    await renderSettings()
    const input = screen.getByRole('spinbutton', { name })

    await user.clear(input)
    await user.type(input, value)
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(put).not.toHaveBeenCalled()
  })

  it('saves the cooldown, daily maximum and lockout numbers before turning the limit and the lockout on', async () => {
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    await renderSettings({
      PasswordResetEmailLimitEnabled: false,
      LoginLockoutEnabled: false,
    })

    await user.click(screen.getByRole('switch', { name: 'Login lockout' }))
    await user.click(
      screen.getByRole('switch', { name: 'Password reset email limit' })
    )
    for (const [name, value] of [
      ['Cooldown (seconds)', '300'],
      ['Emails per address in 24 hours', '3'],
      ['Failed attempts before locking', '5'],
      ['Lock duration (minutes)', '30'],
    ]) {
      const input = screen.getByRole('spinbutton', { name })
      await user.clear(input)
      await user.type(input, value)
    }
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(put).toHaveBeenCalledTimes(6))
    expect(savedKeys(put)).toEqual([
      { key: 'PasswordResetEmailCooldownSeconds', value: 300 },
      { key: 'PasswordResetEmailDailyMax', value: 3 },
      { key: 'LoginLockoutMaxFailures', value: 5 },
      { key: 'LoginLockoutMinutes', value: 30 },
      { key: 'PasswordResetEmailLimitEnabled', value: true },
      { key: 'LoginLockoutEnabled', value: true },
    ])
  })

  it.each([
    ['Password reset captcha', 'PasswordResetCaptchaEnabled'],
    ['Login captcha', 'LoginCaptchaEnabled'],
  ])(
    'saves turning the %s off and drops the cached sign-in status',
    async (name, key) => {
      const put = vi.spyOn(api, 'put')
      const user = userEvent.setup()
      localStorage.setItem(
        'status',
        JSON.stringify({ password_reset_captcha: true, login_captcha: true })
      )
      await renderSettings()

      await user.click(screen.getByRole('switch', { name }))
      await user.click(screen.getByRole('button', { name: 'Save Changes' }))

      await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
      expect(savedKeys(put)).toEqual([{ key, value: false }])
      await waitFor(() => expect(localStorage.getItem('status')).toBeNull())
    }
  )

  it('presents Turnstile as optional because it often fails to load in mainland China', async () => {
    await renderSettings()

    expect(
      screen.getByRole('switch', { name: 'Enable Turnstile' })
    ).toHaveAccessibleDescription(
      'Optional. Cloudflare Turnstile often fails to load for visitors in mainland China, so do not rely on it there: the arithmetic captchas, the reset email limit and the login lockout below work without it.'
    )
    expect(
      screen.queryByText(
        'Protect login and registration with Cloudflare Turnstile'
      )
    ).not.toBeInTheDocument()
  })

  it('says that two-factor authentication does not lift a login lock and names the ways out', async () => {
    await renderSettings()

    const lockout = screen.getByRole('switch', { name: 'Login lockout' })
    expect(lockout).toHaveAccessibleDescription(
      expect.stringContaining(
        'Two-factor authentication stops a guessed password but does not lift a lock.'
      )
    )
    expect(lockout).toHaveAccessibleDescription(
      expect.stringContaining('"Unlock login" in user management')
    )
  })

  it('says that password logins through the API stop working while the login captcha is on', async () => {
    await renderSettings()

    expect(
      screen.getByRole('switch', { name: 'Login captcha' })
    ).toHaveAccessibleDescription(
      'Ask for an arithmetic image captcha on every password login. Scripts and apps that sign in with a username and password through the API stop working while it is on; API keys are not affected.'
    )
  })
})
