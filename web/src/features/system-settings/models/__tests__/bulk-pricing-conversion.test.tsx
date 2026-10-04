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
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import type { ModelPricingBulkConversion } from '@/features/model-pricing/api'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { RatioSettingsCard } from '../ratio-settings-card'

let client: QueryClient | undefined

afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.getState().auth.setUser(null)
  localStorage.clear()
  vi.restoreAllMocks()
})

const emptyOptions = {
  ModelPrice: '{}',
  ModelRatio: '{}',
  CompletionRatio: '{}',
  CacheRatio: '{}',
  CreateCacheRatio: '{}',
  ImageRatio: '{}',
  AudioRatio: '{}',
  AudioCompletionRatio: '{}',
  'billing_setting.billing_mode': '{}',
  'billing_setting.billing_expr': '{}',
  'billing_setting.plugin_billing_expr': '{}',
}

const preview: ModelPricingBulkConversion = {
  dry_run: true,
  converted: [
    { model: 'per-call-model', expression: 'tier("request", fixed(0.35))' },
  ],
  skipped: [
    {
      model: 'gpt-realtime',
      reason: 'Realtime pricing must be converted manually.',
    },
  ],
  suspicious: [
    {
      model: 'claude-sonnet-5',
      expression: 'tier("base", p * 0 + c * 0)',
      legacy_pricing: { ModelPrice: 0.35 },
      replacement: 'tier("request", fixed(0.35))',
      reconverted: false,
    },
  ],
}

function renderCard(result: ModelPricingBulkConversion) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'administrator', role: 100 })
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/option/model_pricing'
          ? { entries: [], options: emptyOptions, empty_version: 'empty' }
          : [],
      vendors: [],
    },
  }))
  const post = vi.spyOn(api, 'post').mockImplementation(async (_url, body) => ({
    data: {
      success: true,
      data: {
        ...result,
        dry_run: (body as { dry_run: boolean }).dry_run,
        suspicious: result.suspicious.map((item) => ({
          ...item,
          reconverted:
            !(body as { dry_run: boolean }).dry_run && !!item.replacement,
        })),
      },
    },
  }))
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <RatioSettingsCard
        modelDefaults={{
          ModelPrice: '{}',
          ModelRatio: '{}',
          CacheRatio: '{}',
          CreateCacheRatio: '{}',
          CompletionRatio: '{}',
          ImageRatio: '{}',
          AudioRatio: '{}',
          AudioCompletionRatio: '{}',
          ExposeRatioEnabled: false,
          BillingMode: '{}',
          BillingExpr: '{}',
          PluginBillingExpr: '{}',
        }}
        groupDefaults={{
          GroupRatio: '{}',
          TopupGroupRatio: '{}',
          UserUsableGroups: '{}',
          GroupGroupRatio: '{}',
          AutoGroups: '[]',
          MaxTokenAutoGroups: 0,
          DefaultUseAutoGroup: false,
          GroupSpecialUsableGroup: '{}',
        }}
        toolPricesDefault='{}'
        visibleTabs={['models']}
      />
    </QueryClientProvider>
  )
  return { get, post }
}

it('previews a dry run, then converts every listed model after confirmation and reloads pricing', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success')
  const { get, post } = renderCard(preview)
  await user.click(
    await screen.findByRole('button', { name: 'Convert all to expressions' })
  )

  const dialog = await screen.findByRole('alertdialog', {
    name: 'Convert all to expressions',
  })
  expect(post).toHaveBeenCalledTimes(1)
  expect(post).toHaveBeenLastCalledWith(
    '/api/option/model_pricing/convert_all',
    { dry_run: true }
  )
  const converted = within(dialog).getByRole('region', {
    name: 'Models to convert',
  })
  expect(converted).toHaveTextContent('per-call-model')
  expect(converted).toHaveTextContent('tier("request", fixed(0.35))')
  const suspicious = within(dialog).getByRole('region', {
    name: 'Free expressions with legacy prices',
  })
  expect(suspicious).toHaveTextContent('claude-sonnet-5')
  expect(suspicious).toHaveTextContent('ModelPrice 0.35')
  expect(
    within(dialog).getByRole('region', { name: 'Skipped models' })
  ).toHaveTextContent('Realtime pricing must be converted manually.')

  const loads = get.mock.calls.filter(
    ([url]) => url === '/api/option/model_pricing'
  ).length
  await user.click(
    within(dialog).getByRole('button', { name: 'Convert 2 models' })
  )
  await waitFor(() =>
    expect(success).toHaveBeenCalledWith(
      'Converted 2 models to billing expressions'
    )
  )
  expect(post).toHaveBeenLastCalledWith(
    '/api/option/model_pricing/convert_all',
    { dry_run: false }
  )
  expect(
    get.mock.calls.filter(([url]) => url === '/api/option/model_pricing').length
  ).toBeGreaterThan(loads)
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
})

it('keeps the confirmation disabled and writes nothing when no model can be converted', async () => {
  const user = userEvent.setup()
  const { post } = renderCard({
    dry_run: true,
    converted: [],
    suspicious: [],
    skipped: preview.skipped,
  })
  await user.click(
    await screen.findByRole('button', { name: 'Convert all to expressions' })
  )
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Convert all to expressions',
  })
  expect(dialog).toHaveTextContent('No legacy prices to convert.')
  expect(
    within(dialog).getByRole('button', { name: 'Convert 0 models' })
  ).toBeDisabled()
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(post).toHaveBeenCalledTimes(1)
})
