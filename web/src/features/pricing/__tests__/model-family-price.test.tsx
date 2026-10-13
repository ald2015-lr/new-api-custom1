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
import { ModelVariantList } from '../components/model-variant-list'
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

describe('lines and prices by group', () => {
  // Anthropic costs twice the 按量 group; model_ratio 1 is $2 / 1M input.
  const ratios = { 按量: 1, Anthropic: 2, kiro: 1 }
  const inGroups = (
    name: string,
    groups: string[],
    overrides: Partial<PricingModel> = {}
  ) => model(name, { enable_groups: groups, group_ratio: ratios, ...overrides })

  function renderList(family: ModelFamily, selectedGroup?: string) {
    return renderWithI18n(
      <ModelVariantList
        family={family}
        perfMap={new Map()}
        priceOptions={{ selectedGroup }}
        onModelClick={vi.fn()}
      />
    )
  }

  it('names an untagged line after its group instead of Default', () => {
    const family = familyOf(
      inGroups('x', ['Anthropic']),
      inGroups('[按量]x', ['按量']),
      inGroups('[按量]x-thinking', ['按量'])
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    expect(
      screen.getAllByRole('listitem').map((chip) => chip.textContent)
    ).toEqual(['Anthropicfrom $4 / 1M', '按量from $2 / 1M'])

    renderList(family)
    expect(screen.getByRole('heading', { name: 'Anthropic' })).toBeVisible()
    expect(screen.queryByText('Default')).toBeNull()
  })

  it('splits a line into one price per group when the groups price it differently', () => {
    const family = familyOf(
      inGroups('x', ['Anthropic', '按量']),
      inGroups('x-thinking', ['Anthropic'])
    )
    renderWithI18n(
      <div data-testid='chips'>
        <FamilyLinePrices family={family} layout='chips' />
      </div>
    )
    expect(
      within(screen.getByTestId('chips'))
        .getAllByRole('listitem')
        .map((chip) => chip.textContent)
    ).toEqual(['按量from $2 / 1M', 'Anthropicfrom $4 / 1M'])

    renderList(family)
    const standard = screen.getByText('Standard').closest('li') as HTMLElement
    const prices = within(standard).getAllByRole('listitem')
    expect(prices.map((price) => price.textContent)).toEqual([
      expect.stringMatching(/^按量Input\$2.*Output\$6/),
      expect.stringMatching(/^AnthropicInput\$4.*Output\$12/),
    ])
  })

  it('names the group of a one-group row in a line spanning several groups', () => {
    const family = familyOf(
      inGroups('x', ['Anthropic', '按量']),
      inGroups('x-thinking', ['Anthropic'])
    )
    renderList(family)
    const thinking = screen.getByText('thinking').closest('li') as HTMLElement
    expect(within(thinking).getByTitle('Anthropic')).toBeVisible()
  })

  it('keeps one chip when every group prices the line the same', () => {
    const family = familyOf(
      inGroups('[按量]x', ['按量', 'kiro']),
      inGroups('[按量]x-thinking', ['按量', 'kiro'])
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    expect(
      screen.getAllByRole('listitem').map((chip) => chip.textContent)
    ).toEqual(['按量from $2 / 1M'])
  })

  it('keeps one unlabelled price per row while a group filter is selected', () => {
    const family = familyOf(
      inGroups('x', ['Anthropic', '按量']),
      inGroups('x-thinking', ['Anthropic', '按量'])
    )
    renderList(family, 'Anthropic')
    const standard = screen.getByText('Standard').closest('li') as HTMLElement
    expect(within(standard).queryByRole('listitem')).toBeNull()
    expect(within(standard).queryByTitle('Anthropic')).toBeNull()
    expect(standard).toHaveTextContent(/Input\$4.*Output\$12/)
  })

  it('keeps Default on an untagged line whose group is also a line tag', () => {
    const family = familyOf(
      inGroups('x', ['按量']),
      inGroups('[按量]x', ['按量'], { model_ratio: 0.5 })
    )
    renderWithI18n(
      <div data-testid='chips'>
        <FamilyLinePrices family={family} layout='chips' />
      </div>
    )
    expect(
      within(screen.getByTestId('chips'))
        .getAllByRole('listitem')
        .map((chip) => chip.textContent)
    ).toEqual(['Default · 按量from $2 / 1M', '按量from $1 / 1M'])

    renderList(family)
    expect(
      screen.getAllByRole('heading', { level: 4 }).map((h) => h.textContent)
    ).toEqual(['Default · 按量', '按量'])
  })

  it('prices a group without a configured ratio at 1 under its own name', () => {
    const family = familyOf(
      inGroups('x', ['按量', 'Anthropic']),
      // vip2 has no ratio, so it bills at 1 like the summary price assumes.
      inGroups('x-thinking', ['vip2'], { model_ratio: 0.1 })
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    expect(
      screen.getAllByRole('listitem').map((chip) => chip.textContent)
    ).toEqual(
      expect.arrayContaining([
        '按量from $2 / 1M',
        'vip2from $0.2 / 1M',
        'Anthropicfrom $4 / 1M',
      ])
    )
  })

  it('does not split a line by group when one of its variants has no group', () => {
    const family = familyOf(
      inGroups('x', ['按量', 'Anthropic']),
      inGroups('x-thinking', [], { model_ratio: 0.1 })
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    expect(
      screen.getAllByRole('listitem').map((chip) => chip.textContent)
    ).toEqual(['from $0.2 / 1M'])
  })

  it('keeps Default on a split untagged group that is also a line tag', () => {
    const family = familyOf(
      inGroups('x', ['按量', 'Anthropic']),
      inGroups('[按量]x', ['按量'], { model_ratio: 0.5 }),
      inGroups('[按量]x-thinking', ['按量'], { model_ratio: 0.5 })
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    expect(
      screen.getAllByRole('listitem').map((chip) => chip.textContent)
    ).toEqual([
      'Default · 按量from $2 / 1M',
      'Anthropicfrom $4 / 1M',
      '按量from $1 / 1M',
    ])
  })

  it('names a split tagged line by its tag and its other groups by tag · group', () => {
    const family = familyOf(
      inGroups('[kiro]x', ['kiro', '按量'], {
        group_ratio: { kiro: 1, 按量: 2 },
      }),
      inGroups('[kiro]x-thinking', ['kiro', '按量'], {
        group_ratio: { kiro: 1, 按量: 2 },
      })
    )
    renderWithI18n(<FamilyLinePrices family={family} layout='chips' />)
    expect(
      screen.getAllByRole('listitem').map((chip) => chip.textContent)
    ).toEqual(['kirofrom $2 / 1M', 'kiro · 按量from $4 / 1M'])
  })

  it('names the groups of a tagged line that differ from its tag', () => {
    const family = familyOf(
      inGroups('[正价kiro]x', ['kiro']),
      inGroups('[按量]x', ['按量'])
    )
    renderList(family)
    expect(screen.getByRole('heading', { name: '正价kiro' })).toBeVisible()
    expect(screen.getByText('Group kiro')).toBeVisible()
    expect(screen.getByRole('heading', { name: '按量' })).toBeVisible()
    expect(screen.queryByText('Group 按量')).toBeNull()
  })
})
