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
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, onTestFinished, vi } from 'vitest'

import { api } from '@/lib/api'

import type { SystemStatus } from '../../types'
import { UserAuthForm } from '../components/user-auth-form'

type ApiReply = { data: Record<string, unknown> }

const clients: QueryClient[] = []
let statusReplies: Partial<SystemStatus>[] = []
let captchaCount = 0
let loginReplies: (() => Promise<ApiReply>)[] = []

const lockMessage =
  'Too many failed sign-in attempts. Please try again in {{minutes}} minutes or reset your password.'

function captchaImage(index: number) {
  return `data:image/png;base64,captcha-${index}`
}

function lockedReply(seconds: number): ApiReply {
  const minutes = Math.ceil(seconds / 60)
  return {
    data: {
      success: false,
      code: 'LOGIN_LOCKED',
      wait_seconds: seconds,
      message: `Too many failed sign-in attempts. Please try again in ${minutes} minutes or reset your password.`,
      message_key: lockMessage,
      message_params: { minutes },
    },
  }
}

const wrongPasswordReply: ApiReply = {
  data: {
    success: false,
    message: 'Username or password is incorrect, or user has been banned',
    message_key: 'Username or password is incorrect, or user has been banned',
  },
}

/** Mocks GET /api/status and /api/captcha. */
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
    throw new Error(`Unexpected GET ${url}`)
  })
}

/** Mocks POST /api/user/login with the queued replies. */
function mockLogin() {
  return vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url !== '/api/user/login') throw new Error(`Unexpected POST ${url}`)
    const reply = loginReplies.shift()
    if (!reply) throw new Error('Unexpected login request')
    return reply()
  })
}

function loginRequests(post: ReturnType<typeof mockLogin>) {
  return post.mock.calls.map(([, body, config]) => ({
    body,
    params: config?.params as Record<string, unknown>,
  }))
}

async function renderSignIn(...statuses: Partial<SystemStatus>[]) {
  statusReplies = statuses
  const root = createRootRoute({ component: Outlet })
  const router = createRouter({
    routeTree: root.addChildren([
      createRoute({
        getParentRoute: () => root,
        path: '/sign-in',
        component: () => <UserAuthForm />,
      }),
      createRoute({
        getParentRoute: () => root,
        path: '/forgot-password',
        component: () => <div>Forgot password page</div>,
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ['/sign-in'] }),
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
  await screen.findByRole('button', { name: 'Sign in' })
}

async function findCaptchaImage(index: number) {
  await waitFor(() =>
    expect(screen.getByRole('img', { name: 'Captcha' })).toHaveAttribute(
      'src',
      captchaImage(index)
    )
  )
}

/** Fills the password login, answering the captcha when it is shown. */
async function fillLogin(
  user: ReturnType<typeof userEvent.setup>,
  username: string,
  answer?: string
) {
  const name = screen.getByLabelText('Username or Email')
  await user.clear(name)
  await user.type(name, username)
  const password = screen.getByLabelText('Password')
  await user.clear(password)
  await user.type(password, 'wrong-password')
  if (answer !== undefined) {
    await findCaptchaImage(captchaCount)
    await user.type(screen.getByRole('textbox', { name: 'Captcha' }), answer)
  }
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  localStorage.clear()
  captchaCount = 0
  loginReplies = []
})

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  localStorage.clear()
  vi.unstubAllGlobals()
})

it('loads no captcha on mount and loads one when the username is focused', async () => {
  const user = userEvent.setup()
  const get = mockGet()
  await renderSignIn({ login_captcha: true })

  expect(get).not.toHaveBeenCalledWith('/api/captcha', expect.anything())
  expect(screen.getByRole('textbox', { name: 'Captcha' })).toBeInTheDocument()
  await user.click(screen.getByLabelText('Username or Email'))

  await findCaptchaImage(1)
  expect(screen.getByLabelText('Username or Email')).toHaveFocus()
})

it('loads the captcha when its answer is focused first and gives the focus back to it', async () => {
  const user = userEvent.setup()
  mockGet()
  await renderSignIn({ login_captcha: true })

  await user.click(screen.getByRole('textbox', { name: 'Captcha' }))

  await findCaptchaImage(1)
  await waitFor(() =>
    expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveFocus()
  )
})

it('sends the captcha with the login and shows a new one after a failed attempt', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  mockGet()
  const post = mockLogin()
  loginReplies = [async () => wrongPasswordReply]
  await renderSignIn({ login_captcha: true })

  await fillLogin(user, 'alice', '12')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))

  await findCaptchaImage(2)
  expect(loginRequests(post)).toEqual([
    {
      body: { username: 'alice', password: 'wrong-password' },
      params: { turnstile: '', captcha_id: 'captcha-1', captcha: '12' },
    },
  ])
  expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveValue('')
  expect(error).toHaveBeenCalledWith(
    'Username or password is incorrect, or user has been banned'
  )
})

it('loads the captcha when autofilled fields are submitted, focuses its answer and sends nothing until it is answered', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  const get = mockGet()
  const post = mockLogin()
  await renderSignIn({ login_captcha: true })
  // Autofill sets the values without focusing the fields.
  fireEvent.change(screen.getByLabelText('Username or Email'), {
    target: { value: 'alice' },
  })
  fireEvent.change(screen.getByLabelText('Password'), {
    target: { value: 'wrong-password' },
  })
  expect(get).not.toHaveBeenCalledWith('/api/captcha', expect.anything())

  await user.click(screen.getByRole('button', { name: 'Sign in' }))

  await findCaptchaImage(1)
  expect(error).toHaveBeenCalledWith('Please complete the captcha.')
  expect(post).not.toHaveBeenCalled()
  await waitFor(() =>
    expect(screen.getByRole('textbox', { name: 'Captcha' })).toHaveFocus()
  )
})

it('shows a lock with the forgot password link, counts down outside the alert and holds the button for that name only', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  const get = mockGet()
  mockLogin()
  loginReplies = [async () => lockedReply(840)]
  await renderSignIn({ login_captcha: true })

  await fillLogin(user, 'alice', '12')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(
    'Too many failed sign-in attempts. Please try again in 14 minutes or reset your password.'
  )
  expect(
    within(alert).getByRole('link', { name: 'Forgot password?' })
  ).toHaveAttribute('href', '/forgot-password')
  const countdown = screen.getByText(/^Try again in 1[34]:\d\d$/)
  expect(countdown).toHaveAttribute('aria-live', 'off')
  expect(alert).not.toContainElement(countdown)
  expect(error).not.toHaveBeenCalled()

  const submit = screen.getByRole('button', { name: 'Sign in' })
  expect(submit).toBeDisabled()
  // The spent challenge is dropped and no image is spent during the lock.
  expect(screen.queryByRole('img', { name: 'Captcha' })).not.toBeInTheDocument()
  expect(get.mock.calls.filter(([url]) => url === '/api/captcha')).toHaveLength(
    1
  )

  const name = screen.getByLabelText('Username or Email')
  await user.clear(name)
  await user.type(name, 'bob')
  expect(submit).toBeEnabled()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()

  await user.clear(name)
  await user.type(name, ' ALICE ')
  expect(submit).toBeDisabled()
  expect(screen.getByRole('alert')).toHaveTextContent('14 minutes')
})

it('clears the lock once its wait has passed', async () => {
  const user = userEvent.setup()
  mockGet()
  mockLogin()
  loginReplies = [async () => lockedReply(60)]
  await renderSignIn({ login_captcha: true })
  await fillLogin(user, 'alice', '12')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  await screen.findByRole('alert')

  const later = Date.now() + 61_000
  vi.spyOn(Date, 'now').mockReturnValue(later)
  act(() => {
    document.dispatchEvent(new Event('visibilitychange'))
  })

  await waitFor(() =>
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  )
  expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
  expect(screen.queryByText(/^Try again in/)).not.toBeInTheDocument()
})

it('re-reads the status and shows a loaded captcha when the server requires one the page did not know about', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  const get = mockGet()
  const post = mockLogin()
  loginReplies = [
    async () => ({
      data: {
        success: false,
        code: 'CAPTCHA_REQUIRED',
        message: 'Please complete the captcha.',
        message_key: 'Please complete the captcha.',
      },
    }),
  ]
  await renderSignIn({ login_captcha: false }, { login_captcha: true })
  expect(
    screen.queryByRole('textbox', { name: 'Captcha' })
  ).not.toBeInTheDocument()

  await fillLogin(user, 'alice')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))

  await waitFor(() =>
    expect(error).toHaveBeenCalledWith('Please complete the captcha.')
  )
  await findCaptchaImage(1)
  expect(loginRequests(post)[0].params).toEqual({
    turnstile: '',
    captcha_id: undefined,
    captcha: undefined,
  })
  expect(get.mock.calls.filter(([url]) => url === '/api/captcha')).toHaveLength(
    1
  )
})

it('loads no captcha and signs in without one while the login captcha is off', async () => {
  const user = userEvent.setup()
  const get = mockGet()
  const post = mockLogin()
  loginReplies = [async () => wrongPasswordReply]
  await renderSignIn({ login_captcha: false })

  await fillLogin(user, 'alice')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))

  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(
    screen.queryByRole('textbox', { name: 'Captcha' })
  ).not.toBeInTheDocument()
  expect(get).not.toHaveBeenCalledWith('/api/captcha', expect.anything())
})

it('starts a passkey login without loading or asking for the captcha', async () => {
  const user = userEvent.setup()
  const error = vi.spyOn(toast, 'error')
  vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
  const credentials = Object.getOwnPropertyDescriptor(navigator, 'credentials')
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { get: vi.fn() },
  })
  onTestFinished(() => {
    if (credentials) {
      Object.defineProperty(navigator, 'credentials', credentials)
    } else {
      Reflect.deleteProperty(navigator, 'credentials')
    }
  })
  const get = mockGet()
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Passkey login is not available' },
  })
  await renderSignIn({ login_captcha: true, passkey_login: true })

  const passkey = await screen.findByRole('button', {
    name: 'Sign in with Passkey',
  })
  await waitFor(() => expect(passkey).toBeEnabled())
  await user.click(passkey)

  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/user/passkey/login/begin',
      undefined,
      expect.anything()
    )
  )
  expect(get).not.toHaveBeenCalledWith('/api/captcha', expect.anything())
  expect(error).not.toHaveBeenCalledWith('Please complete the captcha.')
})
