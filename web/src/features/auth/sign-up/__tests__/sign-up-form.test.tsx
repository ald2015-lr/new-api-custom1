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
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import type { RegisterCaptchaParams, SystemStatus } from '../../types'
import { SignUpForm } from '../components/sign-up-form'

type ApiReply = { data: Record<string, unknown> }

const clients: QueryClient[] = []
let statusReplies: Partial<SystemStatus>[] = []
let captchaCount = 0
// While set, /api/captcha answers only once it resolves.
let captchaGate: Promise<void> | null = null

function captchaImage(index: number) {
  return `data:image/png;base64,captcha-${index}`
}

/** Holds back every /api/captcha answer until the returned call. */
function holdCaptchas() {
  let release = () => {}
  captchaGate = new Promise<void>((resolve) => {
    release = resolve
  })
  return () => {
    captchaGate = null
    release()
  }
}

/** Mocks GET /api/status, /api/captcha and the other GETs a test answers. */
function mockGet(routes: Record<string, () => Promise<ApiReply>> = {}) {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      const status =
        statusReplies.length > 1 ? statusReplies.shift() : statusReplies[0]
      return { data: { success: true, data: status } }
    }
    if (url === '/api/captcha') {
      captchaCount += 1
      const index = captchaCount
      if (captchaGate) await captchaGate
      return {
        data: {
          success: true,
          data: {
            captcha_id: `captcha-${index}`,
            image: captchaImage(index),
            expires_in: 300,
          },
        },
      }
    }
    const route = routes[url]
    if (route) return route()
    throw new Error(`Unexpected GET ${url}`)
  })
}

async function renderSignUp(...statuses: Partial<SystemStatus>[]) {
  statusReplies = statuses
  const root = createRootRoute({ component: Outlet })
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({
        getParentRoute: () => root,
        path: '/sign-up',
        component: SignUpForm,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/sign-in',
        component: () => <div>Sign-in page</div>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ['/sign-up'] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Create account' })
  return { router }
}

async function fillAccount(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Username'), 'new-user')
  await user.type(screen.getByLabelText('Password'), 'password123')
  await user.type(screen.getByLabelText('Confirm password'), 'password123')
}

async function findCaptchaImage(index: number) {
  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Captcha' })).toHaveAttribute(
      'src',
      captchaImage(index)
    )
  )
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
  captchaCount = 0
  captchaGate = null
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  vi.useRealTimers()
})

it('shows no captcha and fetches none when the registration captcha is off', async () => {
  const user = userEvent.setup()
  const get = mockGet()
  await renderSignUp({ register_captcha: false, email_verification: true })
  const email = await screen.findByLabelText(
    'Email (required for verification)'
  )

  expect(
    screen.queryByRole('textbox', { name: 'Captcha' })
  ).not.toBeInTheDocument()
  expect(get).not.toHaveBeenCalledWith('/api/captcha', expect.anything())
  await user.type(email, '123456789@qq.com')
  expect(screen.getByRole('button', { name: 'Send code' })).toBeEnabled()
})

it('keeps registration disabled until the captcha is answered without email verification', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  await fillAccount(user)

  const submit = screen.getByRole('button', { name: 'Create account' })
  expect(submit).toBeDisabled()

  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')
  expect(submit).toBeEnabled()
})

it('sends the captcha with the registration and fetches a new one after the reply', async () => {
  const user = userEvent.setup()
  mockGet()
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Username already exists' },
  })
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  await fillAccount(user)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  const [url, body, config] = post.mock.calls[0]
  expect(url).toBe('/api/user/register')
  expect(body).toMatchObject({ username: 'new-user', password: 'password123' })
  expect(body).not.toHaveProperty('captcha')
  expect(body).not.toHaveProperty('captcha_id')
  expect(config).toEqual({
    params: { turnstile: '', captcha_id: 'captcha-1', captcha: '12' },
  })
  await findCaptchaImage(2)
  expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveValue('')
})

it('loads a new captcha from the image and from the keyboard on the refresh button', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  const answer = screen.getByRole('textbox', { name: 'Captcha' })
  await user.type(answer, '9')

  await user.click(screen.getByRole('button', { name: 'Captcha' }))
  await findCaptchaImage(2)
  expect(answer).toHaveValue('')

  screen.getByRole('button', { name: 'Refresh captcha' }).focus()
  await user.keyboard('{Enter}')
  await findCaptchaImage(3)
})

it('puts the captcha above the email code and requires it before sending the code', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignUp({ register_captcha: true, email_verification: true })
  await findCaptchaImage(1)

  const email = screen.getByLabelText('Email (required for verification)')
  const captcha = screen.getByRole('textbox', { name: 'Captcha' })
  const code = screen.getByPlaceholderText('Verification code')
  expect(
    email.compareDocumentPosition(captcha) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  expect(
    captcha.compareDocumentPosition(code) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()

  await user.type(email, '123456789@qq.com')
  const send = screen.getByRole('button', { name: 'Send code' })
  expect(send).toBeDisabled()

  await user.type(captcha, '7')
  expect(send).toBeEnabled()
})

it('sends the captcha with the email code request and fetches a new one after the reply', async () => {
  const user = userEvent.setup()
  const get = mockGet({
    '/api/verification': async () => ({
      data: {
        success: false,
        code: 'CAPTCHA_INVALID',
        message: 'Captcha is incorrect or has expired. Please try a new one.',
        message_key:
          'Captcha is incorrect or has expired. Please try a new one.',
      },
    }),
  })
  const error = vi.spyOn(toast, 'error')
  await renderSignUp({ register_captcha: true, email_verification: true })
  await findCaptchaImage(1)
  await user.type(
    screen.getByLabelText('Email (required for verification)'),
    '123456789@qq.com'
  )
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '7')
  await user.click(screen.getByRole('button', { name: 'Send code' }))

  await waitFor(() =>
    expect(get).toHaveBeenCalledWith('/api/verification', {
      params: {
        email: '123456789@qq.com',
        turnstile: '',
        captcha_id: 'captcha-1',
        captcha: '7',
      },
    })
  )
  await findCaptchaImage(2)
  expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveValue('')
  expect(error).toHaveBeenCalledWith(
    'Captcha is incorrect or has expired. Please try a new one.'
  )
})

it('registers without the captcha when email verification already required it', async () => {
  const user = userEvent.setup()
  mockGet()
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, message: '' },
  })
  const { router } = await renderSignUp({
    register_captcha: true,
    email_verification: true,
  })
  await findCaptchaImage(1)
  await fillAccount(user)
  await user.type(
    screen.getByLabelText('Email (required for verification)'),
    '123456789@qq.com'
  )
  await user.type(screen.getByPlaceholderText('Verification code'), 'abc123')
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/register',
      expect.objectContaining({ verification_code: 'abc123' }),
      { params: { turnstile: '' } }
    )
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
})

it('re-reads the status and shows the captcha when the server requires one the page did not know about', async () => {
  const user = userEvent.setup()
  mockGet()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      code: 'CAPTCHA_REQUIRED',
      message: 'Please complete the captcha.',
      message_key: 'Please complete the captcha.',
    },
  })
  const error = vi.spyOn(toast, 'error')
  await renderSignUp(
    { register_captcha: false, email_verification: false },
    { register_captcha: true, email_verification: false }
  )
  expect(
    screen.queryByRole('textbox', { name: 'Captcha' })
  ).not.toBeInTheDocument()
  await fillAccount(user)
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await findCaptchaImage(1)
  expect(error).toHaveBeenCalledWith('Please complete the captcha.')
})

it('keeps a spent captcha from being sent again while the next one loads', async () => {
  const user = userEvent.setup()
  mockGet()
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Username already exists' },
  })
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  await fillAccount(user)
  const answer = screen.getByRole('textbox', { name: 'Captcha' })
  const submit = screen.getByRole('button', { name: 'Create account' })
  await user.type(answer, '12')
  const releaseCaptchas = holdCaptchas()
  await user.click(submit)

  await waitFor(() => expect(answer).toBeDisabled())
  expect(screen.queryByRole('img', { name: 'Captcha' })).not.toBeInTheDocument()
  expect(submit).toBeDisabled()
  await user.click(submit)
  expect(post).toHaveBeenCalledTimes(1)

  releaseCaptchas()
  await findCaptchaImage(2)
  await user.type(answer, '34')
  await user.click(submit)
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
  expect(post.mock.calls.map((call) => call[2]?.params)).toEqual([
    { turnstile: '', captcha_id: 'captcha-1', captcha: '12' },
    { turnstile: '', captcha_id: 'captcha-2', captcha: '34' },
  ])
})

it('keeps Send code disabled until the captcha after a rejected one is shown', async () => {
  const user = userEvent.setup()
  const get = mockGet({
    '/api/verification': async () => ({
      data: {
        success: false,
        code: 'CAPTCHA_INVALID',
        message: 'Captcha is incorrect or has expired. Please try a new one.',
        message_key:
          'Captcha is incorrect or has expired. Please try a new one.',
      },
    }),
  })
  await renderSignUp({ register_captcha: true, email_verification: true })
  await findCaptchaImage(1)
  await user.type(
    screen.getByLabelText('Email (required for verification)'),
    '123456789@qq.com'
  )
  const answer = screen.getByRole('textbox', { name: 'Captcha' })
  const send = screen.getByRole('button', { name: 'Send code' })
  await user.type(answer, '7')
  const releaseCaptchas = holdCaptchas()
  await user.click(send)

  await waitFor(() => expect(answer).toBeDisabled())
  expect(send).toBeDisabled()

  releaseCaptchas()
  await findCaptchaImage(2)
  await user.type(answer, '8')
  await user.click(send)
  const sentCaptchas = () =>
    get.mock.calls
      .filter(([url]) => url === '/api/verification')
      .map(([, config]) => {
        const params = config?.params as RegisterCaptchaParams | undefined
        return params?.captcha_id
      })
  await waitFor(() => expect(sentCaptchas()).toHaveLength(2))
  expect(sentCaptchas()).toEqual(['captcha-1', 'captcha-2'])
})

it('drops an answer typed for the old image when the new captcha arrives', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  await fillAccount(user)
  const answer = screen.getByRole('textbox', { name: 'Captcha' })
  const releaseCaptchas = holdCaptchas()

  await user.click(screen.getByRole('button', { name: 'Refresh captcha' }))
  await user.type(answer, '7')
  releaseCaptchas()

  await findCaptchaImage(2)
  expect(answer).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Create account' })).toBeDisabled()
})

it('marks an expired captcha without fetching and loads a new one once the answer is focused', async () => {
  // The fake clock keeps running so queries and lookups still settle.
  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout'],
    shouldAdvanceTime: true,
  })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  mockGet()
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)

  // An idle page keeps its expired challenge, however long it stays open.
  await act(() => vi.advanceTimersByTimeAsync(60 * 60_000))

  expect(
    screen.getByRole('button', { name: 'Expired Click to refresh' })
  ).toBeInTheDocument()
  expect(captchaCount).toBe(1)
  await user.click(screen.getByRole('textbox', { name: 'Captcha' }))
  await findCaptchaImage(2)
  expect(captchaCount).toBe(2)
})

it('gives the focus back to the answer after focusing it replaced an expired captcha', async () => {
  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout'],
    shouldAdvanceTime: true,
  })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  mockGet()
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  await act(() => vi.advanceTimersByTimeAsync(300_000))
  const answer = screen.getByRole('textbox', { name: 'Captcha' })
  const releaseCaptchas = holdCaptchas()

  await user.click(answer)
  expect(answer).toBeDisabled()
  // Browsers take the focus away from a field that becomes disabled.
  act(() => answer.blur())
  releaseCaptchas()

  await findCaptchaImage(2)
  await waitFor(() => expect(answer).toHaveFocus())
})

it('loads a new captcha instead of registering with an expired one', async () => {
  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout'],
    shouldAdvanceTime: true,
  })
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  mockGet()
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, message: '' },
  })
  const error = vi.spyOn(toast, 'error')
  await renderSignUp({ register_captcha: true, email_verification: false })
  await findCaptchaImage(1)
  await fillAccount(user)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')
  await act(() => vi.advanceTimersByTimeAsync(300_000))

  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await findCaptchaImage(2)
  expect(post).not.toHaveBeenCalled()
  expect(error).toHaveBeenCalledWith(
    'The captcha has expired. Please enter the result of the new one.'
  )
  expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveValue('')
})

it('re-reads the status when the server wants a captcha that the cached email verification layout left out', async () => {
  const user = userEvent.setup()
  mockGet()
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      code: 'CAPTCHA_REQUIRED',
      message: 'Please complete the captcha.',
      message_key: 'Please complete the captcha.',
    },
  })
  await renderSignUp(
    { register_captcha: true, email_verification: true },
    { register_captcha: true, email_verification: false }
  )
  await findCaptchaImage(1)
  await fillAccount(user)
  await user.type(
    screen.getByLabelText('Email (required for verification)'),
    '123456789@qq.com'
  )
  await user.type(screen.getByPlaceholderText('Verification code'), 'abc123')
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  // The page switches to the server's layout: the captcha guards the submit,
  // and the challenge it never sent is still good.
  await waitFor(() =>
    expect(
      screen.queryByPlaceholderText('Verification code')
    ).not.toBeInTheDocument()
  )
  expect(captchaCount).toBe(1)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')
  await user.click(screen.getByRole('button', { name: 'Create account' }))
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
  expect(post.mock.calls[1][2]).toEqual({
    params: { turnstile: '', captcha_id: 'captcha-1', captcha: '12' },
  })
})

it('fetches no new captcha after a registration that leaves the page', async () => {
  const user = userEvent.setup()
  mockGet()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, message: '' },
  })
  const { router } = await renderSignUp({
    register_captcha: true,
    email_verification: false,
  })
  await findCaptchaImage(1)
  await fillAccount(user)
  await user.type(screen.getByRole('textbox', { name: 'Captcha' }), '12')

  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
  expect(captchaCount).toBe(1)
})

it('shows the translated network limit message from a 429 registration reply', async () => {
  const user = userEvent.setup()
  mockGet()
  vi.spyOn(api, 'post').mockRejectedValue({
    isAxiosError: true,
    message: 'Request failed with status code 429',
    response: {
      status: 429,
      data: {
        success: false,
        code: 'REGISTER_RATE_LIMITED',
        message:
          'Too many registration attempts from your network. Please try again in 15 minutes.',
        message_key:
          'Too many registration attempts from your network. Please try again in {{minutes}} minutes.',
        message_params: { minutes: 15 },
      },
    },
  })
  const error = vi.spyOn(toast, 'error')
  await renderSignUp({ register_captcha: false, email_verification: false })
  await fillAccount(user)
  const submit = screen.getByRole('button', { name: 'Create account' })
  i18next.addResourceBundle('zhCN', 'translation', zh.translation)
  await act(() => i18next.changeLanguage('zhCN'))
  try {
    await user.click(submit)

    await waitFor(() =>
      expect(error).toHaveBeenCalledWith(
        i18next.t(
          'Too many registration attempts from your network. Please try again in {{minutes}} minutes.',
          { minutes: 15 }
        )
      )
    )
    expect(error.mock.calls[0][0]).toContain('15')
    expect(error.mock.calls[0][0]).not.toMatch(/Too many registration/)
  } finally {
    await act(() => i18next.changeLanguage('en'))
  }
})

it('flags a QQ English alias under the email field and keeps the code unsent', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignUp({ email_verification: true, email_qq_numeric_only: true })
  const email = await screen.findByLabelText(
    'Email (required for verification)'
  )

  await user.type(email, 'alice@qq.com')

  expect(
    await screen.findByText(
      'QQ mail addresses must use the QQ number, for example 123456789@qq.com.'
    )
  ).toBeVisible()
  expect(email).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByRole('button', { name: 'Send code' })).toBeDisabled()

  await user.clear(email)
  await user.type(email, '123456789@qq.com')
  await waitFor(() => expect(email).toHaveAttribute('aria-invalid', 'false'))
  expect(screen.getByRole('button', { name: 'Send code' })).toBeEnabled()
})

it('accepts any address when the server does not require QQ numbers', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignUp({ email_verification: true, email_qq_numeric_only: false })

  await user.type(
    await screen.findByLabelText('Email (required for verification)'),
    'alice@qq.com'
  )

  expect(
    screen.queryByText(
      'QQ mail addresses must use the QQ number, for example 123456789@qq.com.'
    )
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Send code' })).toBeEnabled()
})
