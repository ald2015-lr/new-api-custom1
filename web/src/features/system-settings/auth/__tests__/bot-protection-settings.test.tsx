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
}

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
          <BotProtectionSection defaultValues={defaults} />
        </SettingsPageProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
}

async function renderSettings() {
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
      expect(get).toHaveBeenCalledWith('/api/option/client_ip')
    }
  )
})
