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
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/http-client'
import { INTENT_PREFETCH_DELAY_MS } from '@/lib/idle-prefetch'

import { ModelDetailsContent } from '../components/model-details'
import { prefetchModelDetailsTab } from '../lib/model-details-tab-chunks'
import type { PricingModel } from '../types'

// The Performance tab brings the chart library (about 560 KB) and the API tab
// the code highlighter (about 175 KB). Their chunks are fetched ahead only
// when the pointer or focus rests on a tab, not when it crosses one.

vi.mock('../lib/model-details-tab-chunks', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../lib/model-details-tab-chunks')>()),
  prefetchModelDetailsTab: vi.fn(),
}))

vi.mock('../components/model-details-charts', () => ({
  LatencyTrendChart: () => null,
  UptimeTrendChart: () => null,
}))

vi.mock('../components/model-details-code-sample', () => ({
  ModelDetailsCodeSample: (props: { code: string }) => <pre>{props.code}</pre>,
}))

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

const originalAdapter = api.defaults.adapter
let client: QueryClient

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  api.defaults.adapter = async (config) => ({
    data: { success: true, data: { groups: [] } },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  })
})

afterEach(() => {
  api.defaults.adapter = originalAdapter
  client.clear()
  vi.mocked(prefetchModelDetailsTab).mockClear()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function renderContent() {
  render(
    <QueryClientProvider client={client}>
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
    </QueryClientProvider>
  )
  return {
    performance: screen.getByRole('tab', { name: 'Performance' }),
    apiTab: screen.getByRole('tab', { name: 'API' }),
  }
}

describe('model details tab prefetch', () => {
  test('the pointer resting on a tab prefetches its chunk', () => {
    vi.useFakeTimers()
    const { performance } = renderContent()

    fireEvent.pointerEnter(performance)
    expect(prefetchModelDetailsTab).not.toHaveBeenCalled()
    act(() => vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS))

    expect(prefetchModelDetailsTab).toHaveBeenCalledWith('performance')
  })

  test('crossing the Performance tab on the way to API prefetches only API', () => {
    vi.useFakeTimers()
    const { performance, apiTab } = renderContent()

    fireEvent.pointerEnter(performance)
    act(() => vi.advanceTimersByTime(120))
    fireEvent.pointerLeave(performance)
    fireEvent.pointerEnter(apiTab)
    act(() => vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS * 5))

    expect(vi.mocked(prefetchModelDetailsTab).mock.calls).toEqual([['api']])
  })

  test('focus that moves on before the delay prefetches nothing', () => {
    vi.useFakeTimers()
    const { performance } = renderContent()

    act(() => performance.focus())
    act(() => vi.advanceTimersByTime(80))
    act(() => performance.blur())
    act(() => vi.advanceTimersByTime(INTENT_PREFETCH_DELAY_MS * 5))

    expect(prefetchModelDetailsTab).not.toHaveBeenCalled()
  })

  test('a browser that asks to save data prefetches nothing, and a selected tab still loads', async () => {
    vi.stubGlobal(
      'navigator',
      Object.assign(Object.create(navigator), {
        connection: { saveData: true },
      })
    )
    const user = userEvent.setup()
    const { apiTab } = renderContent()

    await user.hover(apiTab)
    await act(async () => {
      await new Promise((resolve) =>
        setTimeout(resolve, INTENT_PREFETCH_DELAY_MS * 2)
      )
    })
    expect(prefetchModelDetailsTab).not.toHaveBeenCalled()

    await user.click(apiTab)
    expect(
      await screen.findByText(/chat\/completions/, { selector: 'pre' })
    ).toBeVisible()
  })
})
