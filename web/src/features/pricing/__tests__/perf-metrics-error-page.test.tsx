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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, it, vi, type Mock } from 'vitest'

import { api } from '@/lib/api'
import { createAppQueryClient } from '@/lib/query-client'

import { ModelCardGrid } from '../components/model-card-grid'
import { ModelDetailsContent } from '../components/model-details'
import { ModelDetailsPerformance } from '../components/model-details-performance'
import type { PricingModel } from '../types'

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

const model: PricingModel = {
  id: 1,
  model_name: 'example-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 3,
  enable_groups: ['default'],
  group_ratio: { default: 1 },
}

/** The 500 a failing /api/perf-metrics endpoint answers. */
function internalServerError() {
  return new AxiosError(
    'Request failed with status code 500',
    'ERR_BAD_RESPONSE',
    undefined,
    undefined,
    {
      data: '',
      status: 500,
      statusText: '',
      headers: new AxiosHeaders(),
      config: { headers: new AxiosHeaders() },
    }
  )
}

let client: QueryClient
// What the app's query client calls to open the /500 page.
let openErrorPage: Mock<() => void>

beforeEach(() => {
  openErrorPage = vi.fn<() => void>()
  client = createAppQueryClient(openErrorPage)
  vi.spyOn(toast, 'error').mockReturnValue('toast')
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.startsWith('/api/perf-metrics')) throw internalServerError()
    throw new Error(`unexpected request ${url}`)
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
})

function renderWithClient(children: ReactNode) {
  render(<QueryClientProvider client={client}>{children}</QueryClientProvider>)
}

/** Waits until the perf-metrics query with this key has failed. */
async function perfQueryFailed(queryKey: unknown[]) {
  await waitFor(() =>
    expect(client.getQueryState(queryKey)?.status).toBe('error')
  )
}

it('keeps the model square on screen when the card status summary answers 500', async () => {
  renderWithClient(<ModelCardGrid models={[model]} onModelClick={vi.fn()} />)

  await perfQueryFailed(['perf-metrics-summary', 24, ['example-model']])

  expect(openErrorPage).not.toHaveBeenCalled()
  expect(screen.getByText('example-model')).toBeVisible()
})

it('keeps the model details on screen when their metrics summary answers 500', async () => {
  renderWithClient(
    <ModelDetailsContent
      model={model}
      groupRatio={{ default: 1 }}
      usableGroup={{ default: { desc: '', ratio: 1 } }}
      endpointMap={{}}
      autoGroups={[]}
      priceRate={1}
      usdExchangeRate={1}
      tokenUnit='M'
    />
  )

  await perfQueryFailed(['perf-metrics', 'example-model'])

  expect(openErrorPage).not.toHaveBeenCalled()
  expect(screen.getByRole('tab', { name: 'Performance' })).toBeVisible()
})

it('keeps the performance tab on screen when its metrics answer 500', async () => {
  renderWithClient(<ModelDetailsPerformance model={model} />)

  await perfQueryFailed(['perf-metrics', 'example-model'])

  expect(openErrorPage).not.toHaveBeenCalled()
  expect(api.get).toHaveBeenCalledWith('/api/perf-metrics', {
    params: { model: 'example-model', hours: 24 },
  })
})
