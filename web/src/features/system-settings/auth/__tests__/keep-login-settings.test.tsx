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
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance, type i18n } from 'i18next'
import { useState, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { UpdateOptionRequest } from '../../types'
import { BasicAuthSection } from '../basic-auth-section'
import { AuthSettings } from '../index'

const SWITCH_NAME = 'Keep users signed in after restarts'
const SAVED_OFF_NOTE =
  'Saved as off. Users stay signed in until the next restart; that restart and every one after it sign everyone out.'
const SAVED_ON_NOTE =
  'Saved as on, but this run did not start with the kept key, so the next restart still signs everyone out once; after that, users stay signed in. If every restart signs users out, check the server log.'

const basicAuthDefaults = {
  KeepLoginAcrossRestartEnabled: true,
  PasswordLoginEnabled: true,
  PasswordRegisterEnabled: true,
  EmailVerificationEnabled: false,
  RegisterEnabled: true,
  EmailDomainRestrictionEnabled: false,
  EmailAliasRestrictionEnabled: false,
  EmailQQNumericOnlyEnabled: true,
  EmailDomainWhitelist: '',
}

let testI18n: i18n
const clients: QueryClient[] = []

function Providers(props: { children: ReactNode }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  const [client] = useState(() => {
    const created = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    clients.push(created)
    return created
  })
  return (
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <div ref={setContainer} />
        <SettingsPageProvider actionsContainer={container}>
          {props.children}
        </SettingsPageProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
}

/** The basic authentication section as the settings page builds it. */
async function renderSection(saved: boolean, source: string) {
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <Providers>
          <BasicAuthSection
            defaultValues={{
              ...basicAuthDefaults,
              KeepLoginAcrossRestartEnabled: saved,
            }}
            keepLoginSource={source}
          />
        </Providers>
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
}

/**
 * The authentication settings page on a fake server: GET /api/option/
 * returns `options`, and each saved option changes what the next GET returns.
 */
async function renderAuthPage(options: Record<string, string>) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url !== '/api/option/') throw new Error(`Unexpected GET ${url}`)
    return {
      data: {
        success: true,
        message: '',
        data: Object.entries(options).map(([key, value]) => ({ key, value })),
      },
    }
  })
  vi.mocked(api.put).mockImplementation(async (_url, body) => {
    const request = body as UpdateOptionRequest
    options[request.key] = String(request.value)
    return { data: { success: true, message: '' } }
  })
  const root = createRootRoute()
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
  })
  const page = createRoute({
    getParentRoute: () => authenticated,
    path: 'system-settings/auth/$section',
    component: AuthSettings,
  })
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([page])]),
    history: createMemoryHistory({
      initialEntries: ['/system-settings/auth/basic-auth'],
    }),
  })
  render(
    <Providers>
      <RouterProvider router={router} />
    </Providers>
  )
}

function savedOptions() {
  return vi.mocked(api.put).mock.calls.map(([url, body]) => {
    expect(url).toBe('/api/option/')
    return body as UpdateOptionRequest
  })
}

/** The text of every note shown under the switch. */
function notes() {
  return screen.queryAllByRole('status').map((note) => note.textContent)
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
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, message: '' },
  })
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
})

describe('Keep users signed in after restarts', () => {
  it('is the first switch, on by default, with no note while this run uses the kept key', async () => {
    await renderSection(true, 'database')

    const toggle = screen.getByRole('switch', { name: SWITCH_NAME })
    expect(screen.getAllByRole('switch')[0]).toBe(toggle)
    expect(toggle).toBeChecked()
    expect(toggle).not.toHaveAttribute('aria-disabled', 'true')
    expect(notes()).toEqual([])
  })

  it('saves turning it off as a single option update', async () => {
    const user = userEvent.setup()
    await renderSection(true, 'database')

    await user.click(screen.getByRole('switch', { name: SWITCH_NAME }))
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() =>
      expect(savedOptions()).toEqual([
        { key: 'KeepLoginAcrossRestartEnabled', value: false },
      ])
    )
  })

  it('is locked with the SESSION_SECRET note, and left out of saves, while SESSION_SECRET fixes the key', async () => {
    await testI18n.changeLanguage('zh')
    const user = userEvent.setup()
    await renderSection(true, 'env')

    const toggle = screen.getByRole('switch', {
      name: '重启或更新后保持用户登录',
    })
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    expect(notes()).toEqual(['由环境变量 SESSION_SECRET 固定，此开关不生效。'])
    expect(toggle).toHaveAccessibleDescription(
      expect.stringContaining('由环境变量 SESSION_SECRET 固定，此开关不生效。')
    )

    await user.click(toggle)
    expect(toggle).toBeChecked()
    await user.click(screen.getByRole('switch', { name: '密码登录' }))
    await user.click(screen.getByRole('button', { name: '保存更改' }))

    await waitFor(() =>
      expect(savedOptions()).toEqual([
        { key: 'PasswordLoginEnabled', value: false },
      ])
    )
  })

  it.each([
    {
      when: 'saved off while this run uses the kept key',
      source: 'database',
      saved: false,
      expected: [SAVED_OFF_NOTE],
    },
    {
      when: 'saved on while this run uses a random key',
      source: 'random',
      saved: true,
      expected: [SAVED_ON_NOTE],
    },
    {
      when: 'saved off while this run uses a random key',
      source: 'random',
      saved: false,
      expected: [],
    },
    {
      when: 'saved on while the server does not say where the key came from',
      source: '',
      saved: true,
      expected: [],
    },
  ])(
    'tells what the next restart does when $when',
    async ({ source, saved, expected }) => {
      await renderSection(saved, source)

      const toggle = screen.getByRole('switch', { name: SWITCH_NAME })
      expect(toggle).not.toHaveAttribute('aria-disabled', 'true')
      expect(notes()).toEqual(expected)
    }
  )

  it('shows the next-restart note only once turning it off is saved, through the settings page', async () => {
    const user = userEvent.setup()
    await renderAuthPage({
      KeepLoginAcrossRestartEnabled: 'true',
      KeepLoginAcrossRestartSource: 'database',
    })

    const toggle = await screen.findByRole('switch', { name: SWITCH_NAME })
    expect(toggle).toBeChecked()
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
    expect(notes()).toEqual([])

    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(notes()).toEqual([SAVED_OFF_NOTE]))
    expect(savedOptions()).toEqual([
      { key: 'KeepLoginAcrossRestartEnabled', value: false },
    ])
    expect(screen.getByRole('switch', { name: SWITCH_NAME })).not.toBeChecked()
  })
})
