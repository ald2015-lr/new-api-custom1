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
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  AxiosError,
  AxiosHeaders,
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

import { ModelDetails, ModelDetailsContent } from '../components/model-details'
import type { PricingModel } from '../types'

const navigate = vi.hoisted(() => vi.fn())
// The chunks of the Performance and API tabs record when they load.
const loaded = vi.hoisted(() => [] as string[])

vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: ReactNode }) => <div>{props.children}</div>,
}))

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useParams: () => ({ modelId: 'alpha-model' }),
  useSearch: () => ({}),
  useNavigate: () => navigate,
  useRouter: () => ({ state: { location: { href: '/pricing/alpha-model' } } }),
}))

vi.mock('@/lib/auth-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/auth-session')>()),
  refreshAuthentication: vi.fn(),
}))

vi.mock('../components/model-details-charts', () => {
  loaded.push('charts')
  return { LatencyTrendChart: () => null, UptimeTrendChart: () => null }
})

vi.mock('../components/model-details-code-sample', () => {
  loaded.push('code sample')
  return {
    ModelDetailsCodeSample: (props: { code: string }) => (
      <pre>{props.code}</pre>
    ),
  }
})

const model: PricingModel = {
  id: 1,
  model_name: 'alpha-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  group_ratio: { default: 1 },
  supported_endpoint_types: ['openai'],
}

function respond(config: InternalAxiosRequestConfig, data: unknown) {
  return { data, status: 200, statusText: 'OK', headers: {}, config }
}

function fail(config: InternalAxiosRequestConfig, status: number): never {
  throw new AxiosError(
    `Request failed with status code ${status}`,
    status >= 500 ? 'ERR_BAD_RESPONSE' : 'ERR_BAD_REQUEST',
    config,
    undefined,
    {
      data: { success: false, message: 'pricing unavailable' },
      status,
      statusText: '',
      headers: new AxiosHeaders(),
      config,
    }
  )
}

const originalAdapter = api.defaults.adapter
const originalAuth = useAuthStore.getState().auth
let client: QueryClient
let openErrorPage: Mock<() => void>
let pricingStatus: number[]

beforeEach(() => {
  loaded.length = 0
  pricingStatus = []
  openErrorPage = vi.fn<() => void>()
  client = createAppQueryClient(openErrorPage)
  vi.spyOn(toast, 'error').mockReturnValue('toast')
  api.defaults.adapter = async (config) => {
    if (config.url === '/api/pricing') {
      const status = pricingStatus.shift()
      if (status) fail(config, status)
      return respond(config, {
        success: true,
        data: [model],
        vendors: [],
        group_ratio: { default: 1 },
        usable_group: { default: { desc: 'Default', ratio: 1 } },
        supported_endpoint: {
          openai: { path: '/v1/chat/completions', method: 'POST' },
        },
        auto_groups: [],
      })
    }
    if (config.url === '/api/status') {
      return respond(config, { success: true, data: {} })
    }
    if (config.url?.startsWith('/api/perf-metrics')) {
      return respond(config, { success: true, data: { groups: [] } })
    }
    throw new Error(`unexpected request ${config.url}`)
  }
})

afterEach(() => {
  // Unmount first: a page still on screen would react to the resets below.
  cleanup()
  api.defaults.adapter = originalAdapter
  client.clear()
  navigate.mockReset()
  useAuthStore.setState({ auth: originalAuth })
  vi.mocked(refreshAuthentication).mockReset()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function withClient(children: ReactNode) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

function renderContent() {
  return render(
    withClient(
      <ModelDetailsContent
        model={model}
        groupRatio={{ default: 1 }}
        usableGroup={{ default: { desc: 'Default', ratio: 1 } }}
        endpointMap={{
          openai: { path: '/v1/chat/completions', method: 'POST' },
        }}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    )
  )
}

describe('model details tabs', () => {
  test('the charts and the code highlighter load only for their tabs, from resting the pointer or focus on them', async () => {
    const user = userEvent.setup()
    renderContent()
    await screen.findByRole('tab', { name: 'Overview' })
    expect(loaded).toEqual([])

    const performanceTab = screen.getByRole('tab', { name: 'Performance' })
    fireEvent.pointerEnter(performanceTab)
    expect(loaded).toEqual([])
    await waitFor(() => expect(loaded).toEqual(['charts']))
    fireEvent.pointerLeave(performanceTab)

    const apiTab = screen.getByRole('tab', { name: 'API' })
    act(() => apiTab.focus())
    await waitFor(() => expect(loaded).toEqual(['charts', 'code sample']))

    await user.click(apiTab)
    expect(
      await screen.findByText(/chat\/completions/, { selector: 'pre' })
    ).toBeVisible()
  })
})

describe('model details page', () => {
  test('a failed pricing request shows an error with a retry, not "Model not found"', async () => {
    const user = userEvent.setup()
    pricingStatus.push(500)
    render(withClient(<ModelDetails />))

    expect(
      await screen.findByText('Failed to load model pricing')
    ).toBeVisible()
    expect(screen.queryByText('Model not found')).not.toBeInTheDocument()
    expect(openErrorPage).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(
      await screen.findByRole('tab', { name: 'Overview' })
    ).toBeInTheDocument()
  })

  test('a signed-in 401 whose session refresh fails for a moment shows the error with a retry, not the sign-in page', async () => {
    useAuthStore.setState({
      auth: {
        ...originalAuth,
        user: { id: 7, username: 'alice', role: 1 },
        accessToken: 'expired-access-token',
      },
    })
    vi.mocked(refreshAuthentication).mockResolvedValue({
      kind: 'transient_error',
      error: new Error('refresh rate limited'),
    })
    pricingStatus.push(401)
    render(withClient(<ModelDetails />))

    expect(
      await screen.findByText('Failed to load model pricing')
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeVisible()
    expect(navigate).not.toHaveBeenCalled()
  })

  test('an anonymous 401 (sign-in required) sends the visitor to sign in and back', async () => {
    pricingStatus.push(401)
    render(withClient(<ModelDetails />))

    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/sign-in',
        search: { redirect: '/pricing/alpha-model' },
        replace: true,
      })
    )
    expect(refreshAuthentication).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  test('an anonymous 403 (model square switched off) sends the visitor home', async () => {
    pricingStatus.push(403)
    render(withClient(<ModelDetails />))

    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({ to: '/', replace: true })
    )
    expect(toast.error).not.toHaveBeenCalled()
    expect(screen.queryByText('Model not found')).not.toBeInTheDocument()
  })
})
