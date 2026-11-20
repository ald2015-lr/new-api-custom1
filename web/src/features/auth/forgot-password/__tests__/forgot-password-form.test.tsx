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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import type { SystemStatus } from '../../types'
import { ForgotPasswordForm } from '../components/forgot-password-form'

type ApiReply = { data: Record<string, unknown> }

const clients: QueryClient[] = []
let statusReplies: Partial<SystemStatus>[] = []
let captchaCount = 0
let resetReplies: (() => Promise<ApiReply>)[] = []

const cooldownMessage =
  'A password reset email was requested for this address recently. Please wait {{seconds}} seconds and try again. The email can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working.'
const dailyLimitMessage =
  'Too many password reset emails were requested for this address in the last 24 hours. Please try again later or contact the administrator. Emails already sent can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working.'
const sentNote =
  'If an account uses this address, the email can take a few minutes to arrive. Check your spam folder too. If you get several, use the newest one: earlier links stop working.'

function captchaImage(index: number) {
  return `data:image/png;base64,captcha-${index}`
}

function cooldownReply(seconds: number): ApiReply {
  return {
    data: {
      success: false,
      code: 'PASSWORD_RESET_EMAIL_COOLDOWN',
      wait_seconds: seconds,
      message:
        'A password reset email was requested for this address recently.',
      message_key: cooldownMessage,
      message_params: { seconds },
    },
  }
}

function dailyLimitReply(seconds: number): ApiReply {
  return {
    data: {
      success: false,
      code: 'PASSWORD_RESET_EMAIL_DAILY_LIMIT',
      wait_seconds: seconds,
      message: 'Too many password reset emails were requested.',
      message_key: dailyLimitMessage,
    },
  }
}

/** Mocks GET /api/status, /api/captcha and /api/reset_password. */
function mockGet() {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      const status =
        statusReplies.length > 1 ? statusReplies.shift() : statusReplies[0]
      return { data: { success: true, data: status } }
    }
    if (url === '/api/captcha') {
      captchaCount += 1
      return {
        data: {
          success: true,
          data: {
            captcha_id: `captcha-${captchaCount}`,
            image: captchaImage(captchaCount),
            expires_in: 300,
          },
        },
      }
    }
    if (url === '/api/reset_password') {
      const reply = resetReplies.shift()
      if (!reply) throw new Error('Unexpected reset request')
      return reply()
    }
    throw new Error(`Unexpected GET ${url}`)
  })
}

/** The query parameters of every reset email request so far. */
function resetRequests(get: ReturnType<typeof mockGet>) {
  return get.mock.calls
    .filter(([url]) => url === '/api/reset_password')
    .map(([, config]) => config?.params as Record<string, unknown>)
}

async function renderForm(...statuses: Partial<SystemStatus>[]) {
  statusReplies = statuses
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ForgotPasswordForm />
    </QueryClientProvider>
  )
  return screen.findByRole('button', { name: /Send reset email/ })
}

async function findCaptchaImage(index: number) {
  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Captcha' })).toHaveAttribute(
      'src',
      captchaImage(index)
    )
  )
}

/** Types the address and the answer of the shown captcha, then submits. */
async function requestReset(
  user: ReturnType<typeof userEvent.setup>,
  email: string,
  answer = '12'
) {
  const input = screen.getByLabelText('Email')
  await user.clear(input)
  await user.type(input, email)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), answer)
  await user.click(screen.getByRole('button', { name: /Send reset email/ }))
}

beforeEach(() => {
  localStorage.clear()
  captchaCount = 0
  resetReplies = []
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  vi.useRealTimers()
  delete window.turnstile
})

/** Moves the clock by ms and lets the waits on the page catch up. */
function advanceClock(ms: number) {
  const now = Date.now() + ms
  vi.spyOn(Date, 'now').mockReturnValue(now)
  act(() => {
    document.dispatchEvent(new Event('visibilitychange'))
  })
}

it('shows no captcha and fetches none while the reset captcha is off', async () => {
  const user = userEvent.setup()
  const get = mockGet()
  resetReplies = [async () => ({ data: { success: true, message: '' } })]
  const submit = await renderForm({ password_reset_captcha: false })

  expect(
    screen.queryByRole('textbox', { name: 'Captcha' })
  ).not.toBeInTheDocument()
  await user.type(screen.getByLabelText('Email'), 'user@example.com')
  await user.click(submit)

  await waitFor(() => expect(resetRequests(get)).toHaveLength(1))
  expect(resetRequests(get)[0]).toMatchObject({
    email: 'user@example.com',
    captcha_id: undefined,
    captcha: undefined,
  })
  expect(get).not.toHaveBeenCalledWith('/api/captcha', expect.anything())
})

it('shows the captcha and sends no request until it is answered', async () => {
  const user = userEvent.setup()
  const get = mockGet()
  const submit = await renderForm({ password_reset_captcha: true })
  await findCaptchaImage(1)

  await user.type(screen.getByLabelText('Email'), 'user@example.com')
  expect(submit).toBeDisabled()
  await user.type(screen.getByLabelText('Email'), '{Enter}')

  expect(resetRequests(get)).toHaveLength(0)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')
  expect(submit).toBeEnabled()
})

it.each([
  ['an email sent', async () => ({ data: { success: true, message: '' } })],
  ['a cooldown refusal', async () => cooldownReply(95)],
  [
    'a failed request',
    async () => {
      throw new Error('Network Error')
    },
  ],
])(
  'sends the captcha with the request and shows a new one after %s',
  async (_case, reply) => {
    const user = userEvent.setup()
    const get = mockGet()
    resetReplies = [reply]
    await renderForm({ password_reset_captcha: true })
    await findCaptchaImage(1)

    await requestReset(user, 'user@example.com')

    await findCaptchaImage(2)
    expect(resetRequests(get)).toEqual([
      {
        email: 'user@example.com',
        turnstile: '',
        captcha_id: 'captcha-1',
        captcha: '12',
      },
    ])
    expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveValue('')
  }
)

it('shows a cooldown with the spam folder and newest link hints and counts down on the button without a toast', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  mockGet()
  resetReplies = [async () => cooldownReply(95)]
  await renderForm({ password_reset_captcha: true })
  await findCaptchaImage(1)

  await requestReset(user, 'user@example.com')

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(
    'Please wait 95 seconds and try again. The email can take a few minutes to arrive and may be in your spam folder. If you get several, use the newest one: earlier links stop working.'
  )
  expect(
    screen.getByRole('button', { name: /Resend \(9[45]s\)/ })
  ).toBeDisabled()
  expect(error).not.toHaveBeenCalled()
})

it('holds the button for the address the daily limit refused, whatever its case or spaces, and frees it for another address', async () => {
  vi.useFakeTimers({
    toFake: ['Date'],
    now: new Date(2026, 10, 20, 10, 0, 0),
  })
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  mockGet()
  resetReplies = [async () => dailyLimitReply(2 * 3600)]
  await renderForm({ password_reset_captcha: true })
  await findCaptchaImage(1)

  await requestReset(user, 'user@example.com')

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(dailyLimitMessage)
  expect(alert).toHaveTextContent(
    'You can request another reset email for this address after 2026-11-20 12:00.'
  )
  expect(error).not.toHaveBeenCalled()
  await findCaptchaImage(2)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '7')
  const submit = screen.getByRole('button', { name: /Send reset email/ })
  expect(submit).toBeDisabled()

  const email = screen.getByLabelText('Email')
  await user.clear(email)
  await user.type(email, '  USER@Example.com ')
  expect(submit).toBeDisabled()
  expect(screen.getByRole('alert')).toHaveTextContent(dailyLimitMessage)

  await user.clear(email)
  await user.type(email, 'other@example.com')
  expect(submit).toBeEnabled()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('keeps the spam folder note after a sent email clears the form, and waits for the server cooldown only for that address', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success')
  mockGet()
  resetReplies = [async () => ({ data: { success: true, message: '' } })]
  await renderForm({
    password_reset_captcha: true,
    password_reset_email_cooldown: 120,
  })
  await findCaptchaImage(1)

  await requestReset(user, 'user@example.com')

  expect(await screen.findByRole('alert')).toHaveTextContent(sentNote)
  expect(screen.getByLabelText('Email')).toHaveValue('')
  expect(
    screen.getByRole('button', { name: /Resend \((29|30)s\)/ })
  ).toBeDisabled()
  expect(success).toHaveBeenCalledWith(
    'Reset email sent, please check your inbox'
  )

  // The same address, whatever its case or spaces, waits for the server.
  await findCaptchaImage(2)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '7')
  const email = screen.getByLabelText('Email')
  await user.type(email, '  USER@Example.com ')
  expect(
    screen.getByRole('button', { name: /Resend \(1(19|20)s\)/ })
  ).toBeDisabled()

  // Another address only waits the short wait.
  advanceClock(31_000)
  expect(
    screen.getByRole('button', { name: /Resend \(8[89]s\)/ })
  ).toBeDisabled()
  await user.clear(email)
  await user.type(email, 'other@example.com')
  expect(screen.getByRole('button', { name: /Send reset email/ })).toBeEnabled()
})

it('drops the cooldown notice once its wait has passed and keeps the delivery hint', async () => {
  const user = userEvent.setup()
  mockGet()
  resetReplies = [async () => cooldownReply(95)]
  await renderForm({ password_reset_captcha: true })
  await findCaptchaImage(1)
  await requestReset(user, 'user@example.com')
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Please wait 95 seconds'
  )

  advanceClock(85_000)
  expect(
    screen.getByRole('button', { name: /Resend \((9|10)s\)/ })
  ).toBeDisabled()

  advanceClock(11_000)
  await waitFor(() =>
    expect(screen.getByRole('alert')).not.toHaveTextContent('95 seconds')
  )
  expect(screen.getByRole('alert')).toHaveTextContent(sentNote)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '7')
  expect(screen.getByRole('button', { name: /Send reset email/ })).toBeEnabled()
})

it('sends a new Turnstile token with every request', async () => {
  const user = userEvent.setup()
  let widgets = 0
  const renderWidget = vi.fn(
    (_element: HTMLElement, options: Record<string, unknown>) => {
      widgets += 1
      const callback = options.callback as (token: string) => void
      callback(`turnstile-${widgets}`)
    }
  )
  window.turnstile = { render: renderWidget }
  const get = mockGet()
  resetReplies = [
    async () => ({
      data: {
        success: false,
        code: 'CAPTCHA_INVALID',
        message: 'Captcha is incorrect or has expired. Please try a new one.',
        message_key:
          'Captcha is incorrect or has expired. Please try a new one.',
      },
    }),
    async () => ({ data: { success: true, message: '' } }),
  ]
  await renderForm({
    password_reset_captcha: true,
    turnstile_check: true,
    turnstile_site_key: 'site-key',
  })
  await findCaptchaImage(1)

  await requestReset(user, 'user@example.com', '99')
  await findCaptchaImage(2)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')
  await user.click(screen.getByRole('button', { name: /Send reset email/ }))

  await waitFor(() => expect(resetRequests(get)).toHaveLength(2))
  expect(resetRequests(get).map((params) => params.turnstile)).toEqual([
    'turnstile-1',
    'turnstile-2',
  ])
  // A new widget after each request, and none on other renders.
  expect(renderWidget).toHaveBeenCalledTimes(3)
})

it('clears the note when the address is edited', async () => {
  const user = userEvent.setup()
  mockGet()
  resetReplies = [async () => cooldownReply(30)]
  await renderForm({ password_reset_captcha: true })
  await findCaptchaImage(1)
  await requestReset(user, 'user@example.com')
  await screen.findByRole('alert')

  await user.type(screen.getByLabelText('Email'), 'x')

  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('re-reads the status and shows the captcha when the server requires one the page did not know about', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  const get = mockGet()
  resetReplies = [
    async () => ({
      data: {
        success: false,
        code: 'CAPTCHA_REQUIRED',
        message: 'Please complete the captcha.',
        message_key: 'Please complete the captcha.',
      },
    }),
  ]
  const submit = await renderForm(
    { password_reset_captcha: false },
    { password_reset_captcha: true }
  )
  await user.type(screen.getByLabelText('Email'), 'user@example.com')
  await user.click(submit)

  await waitFor(() =>
    expect(error).toHaveBeenCalledWith('Please complete the captcha.')
  )
  await findCaptchaImage(1)
  expect(resetRequests(get)).toHaveLength(1)
  expect(screen.getByLabelText('Email')).toHaveValue('user@example.com')
})
