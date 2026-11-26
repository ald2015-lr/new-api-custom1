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
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  AxiosError,
  AxiosHeaders,
  type AxiosAdapter,
  type InternalAxiosRequestConfig,
} from 'axios'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  test,
  vi,
  type Mock,
} from 'vitest'

import { refreshAuthentication } from '@/lib/auth-session'
import { api } from '@/lib/http-client'
import { createAppQueryClient } from '@/lib/query-client'
import { useAuthStore } from '@/stores/auth-store'

import { Pricing } from '..'
import {
  loadModelDetails,
  prefetchModelDetails,
  prefetchModelDetailsWhenIdle,
} from '../lib/model-details-chunks'
import type { PricingModel } from '../types'

const navigate = vi.hoisted(() => vi.fn())

vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: ReactNode }) => <div>{props.children}</div>,
}))

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useSearch: () => ({}),
  useNavigate: () => navigate,
  useRouter: () => ({ state: { location: { href: '/pricing?view=card' } } }),
}))

// The details chunk never arrives here: the drawer must not wait for it.
vi.mock('../lib/model-details-chunks', () => ({
  loadModelDetails: vi.fn(() => new Promise(() => undefined)),
  prefetchModelDetails: vi.fn(),
  prefetchModelDetailsWhenIdle: vi.fn(() => () => undefined),
}))

vi.mock('@/lib/auth-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/auth-session')>()),
  refreshAuthentication: vi.fn(),
}))

const models: PricingModel[] = ['alpha-model', 'beta-model'].map(
  (name, index) => ({
    id: index + 1,
    model_name: name,
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
  })
)

const pricingPayload = {
  success: true,
  data: models,
  vendors: [],
  group_ratio: { default: 1 },
  usable_group: { default: { desc: 'Default', ratio: 1 } },
  supported_endpoint: {},
  auto_groups: [],
}

function respond(config: InternalAxiosRequestConfig, data: unknown) {
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}

function fail(
  config: InternalAxiosRequestConfig,
  status: number,
  data: unknown
): never {
  throw new AxiosError(
    `Request failed with status code ${status}`,
    status >= 500 ? 'ERR_BAD_RESPONSE' : 'ERR_BAD_REQUEST',
    config,
    undefined,
    { data, status, statusText: '', headers: new AxiosHeaders(), config }
  )
}

const originalAdapter = api.defaults.adapter
const originalAuth = useAuthStore.getState().auth
let client: QueryClient
let openErrorPage: Mock<() => void>
let pricingAnswers: Array<(config: InternalAxiosRequestConfig) => unknown>
let requests: string[]

beforeEach(() => {
  requests = []
  pricingAnswers = []
  openErrorPage = vi.fn<() => void>()
  client = createAppQueryClient(openErrorPage)
  vi.spyOn(toast, 'error').mockReturnValue('toast')
  const adapter: AxiosAdapter = async (config) => {
    requests.push(`${config.method?.toUpperCase()} ${config.url}`)
    if (config.url === '/api/pricing') {
      const answer = pricingAnswers.shift()
      if (!answer) return respond(config, pricingPayload)
      return respond(config, answer(config))
    }
    if (config.url === '/api/status') {
      return respond(config, { success: true, data: { price: 1 } })
    }
    if (config.url?.startsWith('/api/perf-metrics')) {
      return respond(config, { success: true, data: { models: [] } })
    }
    throw new Error(`unexpected request ${config.url}`)
  }
  api.defaults.adapter = adapter
})

afterEach(() => {
  api.defaults.adapter = originalAdapter
  client.clear()
  navigate.mockReset()
  useAuthStore.setState({ auth: originalAuth })
  vi.mocked(refreshAuthentication).mockReset()
  vi.mocked(prefetchModelDetails).mockClear()
  vi.mocked(prefetchModelDetailsWhenIdle).mockClear()
  vi.restoreAllMocks()
})

function renderPricing() {
  return render(
    <QueryClientProvider client={client}>
      <Pricing />
    </QueryClientProvider>
  )
}

describe('model square list', () => {
  test('pointing at or focusing a model starts loading its details, and the list prefetches them at idle', async () => {
    renderPricing()
    await screen.findByText('alpha-model')
    expect(prefetchModelDetailsWhenIdle).toHaveBeenCalledTimes(1)
    expect(prefetchModelDetails).not.toHaveBeenCalled()

    const [details] = screen.getAllByRole('button', { name: 'Details' })
    fireEvent.pointerOver(details)
    expect(prefetchModelDetails).toHaveBeenCalledTimes(1)
    act(() => details.focus())
    expect(prefetchModelDetails).toHaveBeenCalledTimes(2)
  })

  test('the details drawer opens at once with a placeholder while its chunk loads', async () => {
    const user = userEvent.setup()
    renderPricing()
    await screen.findByText('alpha-model')

    await user.click(screen.getAllByRole('button', { name: 'Details' })[0])

    const drawer = await screen.findByRole('dialog')
    expect(within(drawer).getByText('alpha-model')).toBeInTheDocument()
    expect(drawer.querySelector('[aria-busy="true"]')).not.toBeNull()
    expect(loadModelDetails).toHaveBeenCalled()
  })
})

describe('model square errors', () => {
  test('a failed pricing request shows an error with a retry, not an empty list, and stays on the page', async () => {
    const user = userEvent.setup()
    pricingAnswers.push((config) =>
      fail(config, 500, { success: false, message: 'database is busy' })
    )
    renderPricing()

    expect(
      await screen.findByText('Failed to load model pricing')
    ).toBeVisible()
    expect(screen.getByText('database is busy')).toBeVisible()
    expect(screen.queryByText('No models found')).not.toBeInTheDocument()
    expect(
      screen.queryByText(/This site currently has/)
    ).not.toBeInTheDocument()
    expect(openErrorPage).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByText('alpha-model')).toBeVisible()
    expect(
      screen.queryByText('Failed to load model pricing')
    ).not.toBeInTheDocument()
  })

  test.each([
    {
      status: 401,
      why: 'sign-in is required',
      to: { to: '/sign-in', search: { redirect: '/pricing?view=card' } },
    },
    { status: 403, why: 'the model square is switched off', to: { to: '/' } },
  ])(
    'an anonymous $status ($why) redirects without a session refresh or a toast',
    async ({ status, to }) => {
      pricingAnswers.push((config) =>
        fail(config, status, { success: false, message: 'refused' })
      )
      renderPricing()

      await waitFor(() =>
        expect(navigate).toHaveBeenCalledWith({ ...to, replace: true })
      )
      expect(refreshAuthentication).not.toHaveBeenCalled()
      expect(toast.error).not.toHaveBeenCalled()
      expect(screen.queryByText('Failed to load model pricing')).toBeNull()
      expect(requests.filter((r) => r === 'GET /api/pricing')).toHaveLength(1)
    }
  )

  test('a signed-in 401 still refreshes the session and retries', async () => {
    useAuthStore.setState({
      auth: { ...originalAuth, accessToken: 'expired-access-token' },
    })
    vi.mocked(refreshAuthentication).mockResolvedValue({
      kind: 'authenticated',
      bundle: {} as never,
    })
    pricingAnswers.push((config) =>
      fail(config, 401, { success: false, message: 'expired' })
    )
    renderPricing()

    expect(await screen.findByText('alpha-model')).toBeVisible()
    expect(refreshAuthentication).toHaveBeenCalledTimes(1)
    expect(navigate).not.toHaveBeenCalled()
  })
})
