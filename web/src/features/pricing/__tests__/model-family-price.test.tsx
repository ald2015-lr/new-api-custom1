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
import { act, render, screen, within } from '@testing-library/react'
import { createInstance } from 'i18next'
import type { ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { ModelFamilyCard } from '../components/model-family-card'
import {
  FamilyLinePrices,
  VariantPrice,
} from '../components/model-family-price'
import {
  DEFAULT_MODEL_VARIANT_RULES,
  groupModelFamilies,
  type ModelFamily,
} from '../lib/model-variants'
import type { PricingModel } from '../types'

const TIME_OF_DAY_EXPR =
  'hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12 ? tier("peak", p * 3 + c * 9) : tier("off_peak", p * 1.5 + c * 4.5)'

// Same interpolation settings as the app: React escapes, i18next does not.
const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({
    lng: 'en',
    resources: { en: { translation: {} } },
    interpolation: { escapeValue: false },
  })
})

function renderWithI18n(node: ReactNode) {
  return render(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>)
}

let nextId = 1
function model(name: string, overrides: Partial<PricingModel> = {}) {
  return {
    id: nextId++,
    model_name: name,
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 3,
    enable_groups: ['default'],
    group_ratio: { default: 1 },
    ...overrides,
  } satisfies PricingModel
}

function familyOf(...models: PricingModel[]): ModelFamily {
  const [family] = groupModelFamilies(models, DEFAULT_MODEL_VARIANT_RULES)
  return family
}

beforeEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: 'USD' },
  })
})

afterEach(() => {
  vi.useRealTimers()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

describe('time-of-day prices in family views', () => {
  it('refreshes the line chip and the variant row at the period boundary', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-07T08:59:59+08:00'))
    const timed = {
      billing_mode: 'tiered_expr',
      billing_expr: TIME_OF_DAY_EXPR,
    }
    const family = familyOf(
      model('[按量]x', timed),
      model('[按量]x-thinking', timed)
    )
    renderWithI18n(
      <>
        <div data-testid='chips'>
          <FamilyLinePrices family={family} layout='chips' />
        </div>
        <div data-testid='row'>
          <VariantPrice model={family.variants[0].model} />
        </div>
      </>
    )
    const chips = screen.getByTestId('chips')
    const row = screen.getByTestId('row')
    expect(chips).toHaveTextContent('from $1.5 / 1M')
    expect(row).toHaveTextContent(/\$1\.5.*\$4\.5/)
    expect(within(row).getByText('Current period price')).toBeVisible()

    act(() => vi.advanceTimersByTime(1000))

    expect(chips).toHaveTextContent('from $3 / 1M')
    expect(row).toHaveTextContent(/\$3.*\$9/)
  })
})

describe('family lines without a comparable price', () => {
  it.each([
    {
      reason: 'unconfigured task usage',
      overrides: {
        quota_type: 2,
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      },
      text: 'Not configured',
    },
    {
      reason: 'a special billing expression',
      overrides: {
        billing_mode: 'tiered_expr',
        billing_expr: 'param("n") > 1 ? tier("a", p * 2) : tier("b", p * 1)',
      },
      text: 'Special billing expression',
    },
  ] satisfies {
    reason: string
    overrides: Partial<PricingModel>
    text: string
  }[])(
    'labels a line of $reason like its variant rows',
    ({ overrides, text }) => {
      const family = familyOf(
        model('[按次]vid', overrides),
        model('[按次]vid-thinking', overrides)
      )
      renderWithI18n(
        <>
          <div data-testid='chips'>
            <FamilyLinePrices family={family} layout='chips' />
          </div>
          <div data-testid='list'>
            <FamilyLinePrices family={family} layout='list' />
          </div>
          <div data-testid='row'>
            <VariantPrice model={family.variants[0].model} />
          </div>
        </>
      )
      expect(screen.getByTestId('chips')).toHaveTextContent(`按次${text}`)
      expect(screen.getByTestId('list')).toHaveTextContent(`按次${text}`)
      expect(screen.getByTestId('row')).toHaveTextContent(text)
      expect(screen.queryByText('Dynamic Pricing')).toBeNull()
    }
  )
})

describe('line chips', () => {
  it('truncates only a long line name and keeps the whole price', () => {
    const line = '超长线路名称-企业专线-高可用'
    const family = familyOf(
      model(`[${line}]x`, { quota_type: 1, model_price: 0.5 }),
      model(`[${line}]x-thinking`, { quota_type: 1, model_price: 0.5 })
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    const name = screen.getByText(line)
    const price = screen.getByText('from $0.5 / request')
    expect(name).toHaveClass('truncate')
    expect(price).toHaveClass('shrink-0')
    expect(price).not.toHaveClass('truncate')
  })
})

describe('table parent price', () => {
  it('shows each line as a chip with a bare lowest price and a currency caption like its rows', () => {
    const family = familyOf(
      model('[按次]x', { quota_type: 1, model_price: 0.9 }),
      model('[按次]x-thinking', { quota_type: 1, model_price: 1.2 }),
      model('[按量]x', { model_ratio: 7.5 })
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='list' />)
    const lines = screen.getAllByRole('listitem')
    expect(lines.map((line) => line.textContent)).toEqual([
      '按次from 0.9USD / request',
      '按量from 15USD / 1M tokens',
    ])
    expect(within(lines[0]).getByTitle('按次')).toHaveTextContent('按次')
    expect(within(lines[1]).getByTitle('按量')).toHaveTextContent('按量')
  })
})

describe('variant price details', () => {
  it('adds the cached input price of a token model with a cache ratio', () => {
    renderWithI18n(
      <VariantPrice
        model={model('x', { model_ratio: 7.5, cache_ratio: 0.1 })}
      />
    )
    expect(screen.getByText('Cached')).toBeVisible()
    expect(screen.getByText('$1.5')).toBeVisible()
  })

  it('leaves the cached price out when the model has no cache ratio', () => {
    renderWithI18n(<VariantPrice model={model('x', { model_ratio: 7.5 })} />)
    expect(screen.queryByText('Cached')).toBeNull()
  })
})

describe('family card summary', () => {
  it('shows the union of the variants groups and endpoints', () => {
    const family = familyOf(
      model('[按次]x', {
        enable_groups: ['default'],
        supported_endpoint_types: ['openai'],
      }),
      model('[按量]x-thinking', {
        enable_groups: ['default', 'vip'],
        supported_endpoint_types: ['openai', 'anthropic'],
      })
    )
    renderWithI18n(
      <ModelFamilyCard
        family={family}
        expanded={false}
        panelId='panel'
        onExpandedChange={vi.fn()}
        perfMap={new Map()}
        priceOptions={{}}
      />
    )
    const groups = screen.getByText('Groups').parentElement
    const endpoints = screen.getByText('Endpoints').parentElement
    expect(groups).toHaveTextContent('Groupsdefault+1')
    expect(within(groups as HTMLElement).getByText('+1')).toHaveAttribute(
      'title',
      'vip'
    )
    expect(endpoints).toHaveTextContent('Endpointsopenai, anthropic')
  })
})
