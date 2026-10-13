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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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

import { ModelCardGrid } from '../components/model-card-grid'
import { PricingTable } from '../components/pricing-table'
import { PricingToolbar } from '../components/pricing-toolbar'
import { aggregateModelPerf } from '../lib/model-family'
import {
  DEFAULT_MODEL_VARIANT_RULES,
  groupModelFamilies,
  parseModelName,
  resolveModelSquareSettings,
  validateVariantRules,
} from '../lib/model-variants'
import type { PricingModel } from '../types'

const RULES = DEFAULT_MODEL_VARIANT_RULES

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
    quota_type: 1,
    model_ratio: 1,
    completion_ratio: 1,
    model_price: 1,
    enable_groups: ['default'],
    group_ratio: { default: 1 },
    ...overrides,
  } satisfies PricingModel
}

/** The user's catalog example, in the order a name sort produces. */
function claudeCatalog(): PricingModel[] {
  return [
    model('[按次]claude-opus-4-6', { model_price: 0.9 }),
    model('[按次]claude-opus-4-6-thinking'),
    model('[按次]claude-opus-4-6-thinking-high', { model_price: 1.2 }),
    model('[按次]claude-opus-4-6-thinking-low'),
    model('[按次]claude-opus-4-6-thinking-max', { model_price: 1.5 }),
    model('[按量]claude-opus-4-6', {
      quota_type: 0,
      model_ratio: 7.5,
      completion_ratio: 5,
    }),
    model('[正价kiro]claude-opus-4-6-thinking-high', { model_price: 0.25 }),
    model('[正价kiro]claude-opus-4-6', { model_price: 0.2 }),
    model('[正价kiro]claude-sonnet-4-6-thinking-high', { model_price: 0.15 }),
  ]
}

describe('parseModelName', () => {
  it.each([
    {
      name: '[按次]claude-opus-4-6-thinking-high',
      line: '按次',
      base: 'claude-opus-4-6',
      suffixes: ['-thinking-high'],
      label: 'thinking-high',
    },
    {
      name: '[按次]gemini-3-pro-preview-128-nothinking',
      line: '按次',
      base: 'gemini-3-pro-preview',
      suffixes: ['-128', '-nothinking'],
      label: '128 · nothinking',
    },
    {
      name: '[按量]claude-fable-5-thinking',
      line: '按量',
      base: 'claude-fable-5',
      suffixes: ['-thinking'],
      label: 'thinking',
    },
    {
      name: 'gemini-3.1-pro-preview',
      line: '',
      base: 'gemini-3.1-pro-preview',
      suffixes: [],
      label: '',
    },
  ])(
    'splits $name into line "$line", base $base and label "$label"',
    (expected) => {
      const parsed = parseModelName(expected.name, RULES)
      expect(parsed).toMatchObject(expected)
    }
  )

  it('joins the descriptions of stacked -128 and -nothinking suffixes in name order', () => {
    expect(
      parseModelName('[按次]gemini-3-pro-preview-128-nothinking', RULES)
        .description
    ).toBe('思考预算 128（最低思考强度）；会思考，但不返回思考内容')
  })

  it('prefers the longest matching suffix so -thinking-high is not read as -high', () => {
    const parsed = parseModelName('claude-opus-4-6-thinking-high', RULES)
    expect(parsed.ruleIndexes).toEqual([
      RULES.findIndex((rule) => rule.suffix === '-thinking-high'),
    ])
  })

  it.each(['-thinking', 'model--thinking'])(
    'keeps %s whole when stripping would leave an empty base or one ending in a dash',
    (name) => {
      expect(parseModelName(name, RULES)).toMatchObject({
        base: name,
        suffixes: [],
      })
    }
  )
})

describe('groupModelFamilies', () => {
  it('puts every line of a base model into one family ordered 按次, 按量, then other lines', () => {
    const [opus] = groupModelFamilies(claudeCatalog(), RULES)
    expect(opus.base).toBe('claude-opus-4-6')
    expect(opus.lines.map((line) => line.line)).toEqual([
      '按次',
      '按量',
      '正价kiro',
    ])
    expect(opus.variants).toHaveLength(8)
  })

  it('orders variants inside a line by rule order with the standard variant first', () => {
    const [opus] = groupModelFamilies(claudeCatalog(), RULES)
    expect(opus.lines[0].variants.map((v) => v.parsed.label)).toEqual([
      '',
      'thinking',
      'thinking-low',
      'thinking-high',
      'thinking-max',
    ])
    expect(opus.lines[2].variants.map((v) => v.model.model_name)).toEqual([
      '[正价kiro]claude-opus-4-6',
      '[正价kiro]claude-opus-4-6-thinking-high',
    ])
  })

  it('orders gemini variants as standard, nothinking, 128, then 128 · nothinking', () => {
    const [gemini] = groupModelFamilies(
      [
        model('[按次]gemini-3-pro-preview-128-nothinking'),
        model('[按次]gemini-3-pro-preview-nothinking'),
        model('[按次]gemini-3-pro-preview-128'),
        model('[按次]gemini-3-pro-preview'),
      ],
      RULES
    )
    expect(gemini.variantLabels).toEqual([
      '',
      'nothinking',
      '128',
      '128 · nothinking',
    ])
  })

  it('lists the untagged line before tagged lines', () => {
    const [family] = groupModelFamilies(
      [model('[按次]gpt-5.5'), model('gpt-5.5-thinking')],
      RULES
    )
    expect(family.lines.map((line) => line.line)).toEqual(['', '按次'])
  })

  it.each([
    ['gpt-5.1-codex', 'gpt-5.1-codex-max'],
    ['qwen', 'qwen-max'],
    ['o4-mini', 'o4-mini-high'],
  ])(
    'keeps %s and %s apart because the built-in rules fold no generic effort suffix',
    (plain, effort) => {
      const families = groupModelFamilies([model(plain), model(effort)], RULES)
      expect(families.map((family) => family.base)).toEqual([plain, effort])
    }
  )

  it('keeps families in the order their first member appears in the sorted list', () => {
    const families = groupModelFamilies(
      [
        model('[按次]gemini-3-pro-preview-128'),
        model('[按次]claude-opus-4-6'),
        model('[按次]gemini-3-pro-preview'),
      ],
      RULES
    )
    expect(families.map((family) => family.base)).toEqual([
      'gemini-3-pro-preview',
      'claude-opus-4-6',
    ])
  })

  it('leaves a base model with one name as a single-member family', () => {
    const families = groupModelFamilies(claudeCatalog(), RULES)
    const sonnet = families.find((f) => f.base === 'claude-sonnet-4-6')
    expect(sonnet?.models.map((m) => m.model_name)).toEqual([
      '[正价kiro]claude-sonnet-4-6-thinking-high',
    ])
  })

  it('still groups the lines of one base model when the rule list is empty', () => {
    const families = groupModelFamilies(claudeCatalog(), [])
    expect(
      families.map((family) => family.models.map((m) => m.model_name))
    ).toContainEqual([
      '[按次]claude-opus-4-6',
      '[按量]claude-opus-4-6',
      '[正价kiro]claude-opus-4-6',
    ])
    expect(
      families
        .find((family) => family.base === 'claude-opus-4-6-thinking-high')
        ?.lines.map((line) => line.line)
    ).toEqual(['按次', '正价kiro'])
  })
})

describe('resolveModelSquareSettings', () => {
  it('enables grouping with the built-in rules when the backend omits model_square', () => {
    expect(resolveModelSquareSettings(undefined)).toEqual({
      variant_grouping_enabled: true,
      variant_rules: [...DEFAULT_MODEL_VARIANT_RULES],
    })
  })

  it('turns grouping off when the backend disables it', () => {
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: false,
        variant_rules: [],
      }).variant_grouping_enabled
    ).toBe(false)
  })

  it('keeps an empty rule list sent as null instead of restoring the defaults', () => {
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: null,
      }).variant_rules
    ).toEqual([])
  })

  it('falls back to the built-in rules when the payload rules are malformed', () => {
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: 'not-an-array',
      }).variant_rules
    ).toEqual([...DEFAULT_MODEL_VARIANT_RULES])
  })
})

describe('validateVariantRules', () => {
  it('accepts the built-in rules', () => {
    expect(validateVariantRules(DEFAULT_MODEL_VARIANT_RULES)).toEqual([])
  })

  it('reports empty, spaced and duplicate suffixes and empty or long labels per field', () => {
    const issues = validateVariantRules([
      { suffix: '', label: 'a' },
      { suffix: '-a b', label: 'b' },
      { suffix: '-x', label: '' },
      { suffix: '-x', label: 'x'.repeat(65) },
    ])
    expect(
      issues.map((issue) => [issue.index, issue.field, issue.message])
    ).toEqual([
      [0, 'suffix', 'Suffix is required'],
      [1, 'suffix', 'Suffix must not contain spaces'],
      [2, 'label', 'Label is required'],
      [3, 'suffix', 'Duplicate suffix'],
      [3, 'label', 'Must be {{max}} characters or fewer'],
    ])
  })

  it.each([
    ['NEL', '-a\u0085b'],
    ['no-break space', '-a\u00a0b'],
    ['ideographic space', '-a\u3000b'],
  ])(
    'rejects a suffix with a %s like the backend unicode.IsSpace check',
    (_name, suffix) => {
      expect(validateVariantRules([{ suffix, label: 'a' }])).toEqual([
        expect.objectContaining({ message: 'Suffix must not contain spaces' }),
      ])
    }
  )

  it('accepts a suffix with a zero-width no-break space, which Go does not treat as space', () => {
    expect(validateVariantRules([{ suffix: '-a\ufeffb', label: 'a' }])).toEqual(
      []
    )
  })

  it('counts lengths in code points so 64 emoji fit the label limit and 65 do not', () => {
    const emoji = '\u{1F9E0}'
    expect(
      validateVariantRules([
        {
          suffix: '-a',
          label: emoji.repeat(64),
          description: emoji.repeat(200),
        },
      ])
    ).toEqual([])
    expect(
      validateVariantRules([{ suffix: '-a', label: emoji.repeat(65) }])
    ).toEqual([
      expect.objectContaining({
        field: 'label',
        message: 'Must be {{max}} characters or fewer',
      }),
    ])
  })

  it('treats a label of Unicode spaces as blank, like Go strings.TrimSpace', () => {
    expect(
      validateVariantRules([{ suffix: '-a', label: '\u3000\u0085 ' }])
    ).toEqual([expect.objectContaining({ message: 'Label is required' })])
  })
})

describe('aggregateModelPerf', () => {
  const hour = 1_800_000_000 - (1_800_000_000 % 3600)

  it('averages success rates and takes the median latency without request counts', () => {
    const perf = aggregateModelPerf([
      { success_rate: 100, avg_latency_ms: 1000, avg_tps: 10 },
      { success_rate: 90, avg_latency_ms: 2000, avg_tps: 20 },
      { success_rate: 80, avg_latency_ms: 9000, avg_tps: 30 },
      undefined,
    ])
    expect(perf?.success_rate).toBe(90)
    expect(perf?.avg_latency_ms).toBe(2000)
    expect(perf?.avg_tps).toBe(20)
  })

  it('weights the success rate by request counts when every variant reports one', () => {
    const perf = aggregateModelPerf([
      { success_rate: 100, avg_latency_ms: 1, avg_tps: 1, request_count: 3 },
      { success_rate: 60, avg_latency_ms: 1, avg_tps: 1, request_count: 1 },
    ])
    expect(perf?.success_rate).toBe(90)
  })

  it('shows the worst variant in an hour where one variant is failing badly', () => {
    const perf = aggregateModelPerf([
      {
        success_rate: 100,
        avg_latency_ms: 1,
        avg_tps: 1,
        window_start: hour,
        recent_success_series: [
          { ts: hour, success_rate: 100 },
          { ts: hour + 3600, success_rate: 100 },
        ],
      },
      {
        success_rate: 50,
        avg_latency_ms: 1,
        avg_tps: 1,
        recent_success_series: [
          { ts: hour, success_rate: 90 },
          { ts: hour + 3600, success_rate: 20 },
        ],
      },
    ])
    expect(perf?.window_start).toBe(hour)
    expect(perf?.recent_success_series).toEqual([
      { ts: hour, success_rate: 95 },
      { ts: hour + 3600, success_rate: 20 },
    ])
  })

  it('returns no metrics when no variant has any', () => {
    expect(aggregateModelPerf([undefined, undefined])).toBeUndefined()
  })
})

describe('family cards', () => {
  let queryClient: QueryClient

  beforeEach(() => {
    useSystemConfigStore.getState().setConfig({
      currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: 'USD' },
    })
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    queryClient.setQueryData(['perf-metrics-summary', 24], {
      success: true,
      data: { models: [] },
    })
  })
  afterEach(() => {
    queryClient.clear()
    useSystemConfigStore
      .getState()
      .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  })

  function withQuery(node: ReactNode) {
    return (
      <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
    )
  }

  function renderGrid(models: PricingModel[], onModelClick = vi.fn()) {
    renderWithI18n(
      withQuery(
        <ModelCardGrid
          models={models}
          families={groupModelFamilies(models, RULES)}
          onModelClick={onModelClick}
        />
      )
    )
    return onModelClick
  }

  function familyCard(base: string) {
    const card = screen
      .getByRole('heading', { level: 3, name: base })
      .closest<HTMLElement>('[data-slot=card]')
    if (!card) throw new Error(`No card for ${base}`)
    return card
  }

  it('shows one collapsed card per family with the lowest price of each line', () => {
    renderGrid(claudeCatalog())
    const card = familyCard('claude-opus-4-6')
    const lines = within(card).getByRole('group', { name: 'Lines' })
    expect(
      within(lines)
        .getAllByRole('listitem')
        .map((item) => item.textContent)
    ).toEqual([
      '按次from $0.9 / request',
      '按量from $15 / 1M',
      '正价kirofrom $0.2 / request',
    ])
    const variants = within(card).getByRole('group', { name: 'Variants' })
    expect(within(variants).getByText('Standard')).toBeVisible()
    expect(within(variants).getByText('+1')).toHaveAttribute(
      'title',
      'thinking-max'
    )
    expect(within(card).getByText('Variants: 8')).toBeVisible()
  })

  it('renders a single-member family as the plain model card with its full name', () => {
    renderGrid(claudeCatalog())
    expect(
      screen.getByRole('heading', {
        level: 3,
        name: '[正价kiro]claude-sonnet-4-6-thinking-high',
      })
    ).toBeVisible()
    expect(screen.getAllByRole('heading', { level: 3 })).toHaveLength(2)
  })

  it('expands a family into a variant panel grouped by line and collapses it again', async () => {
    const user = userEvent.setup()
    renderGrid(claudeCatalog())
    const card = familyCard('claude-opus-4-6')
    const toggle = within(card).getByRole('button', {
      name: 'Expand claude-opus-4-6',
    })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('region')).toBeNull()

    await user.click(toggle)

    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle).toHaveAccessibleName('Collapse claude-opus-4-6')
    const panel = screen.getByRole('region', {
      name: 'claude-opus-4-6 Variants: 8',
    })
    expect(toggle).toHaveAttribute('aria-controls', panel.id)
    const lines = within(panel).getAllByRole('group')
    expect(lines.map((line) => line.getAttribute('aria-labelledby'))).toEqual(
      lines.map((line) => within(line).getByRole('heading', { level: 4 }).id)
    )
    expect(
      lines.map((line) => within(line).getByRole('heading').textContent)
    ).toEqual(['按次', '按量', '正价kiro'])
    const tokenLine = within(lines[1]).getByRole('listitem')
    expect(tokenLine).toHaveTextContent(/Input\s*\$15\s*Output\s*\$75\s*\/ 1M/)
    const kiroRows = within(lines[2]).getAllByRole('listitem')
    expect(kiroRows.map((row) => row.textContent)).toEqual([
      expect.stringContaining('Standard$0.2/ request'),
      expect.stringContaining('thinking-high$0.25/ request'),
    ])

    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).not.toHaveAttribute('aria-controls')
    expect(screen.queryByRole('region')).toBeNull()
  })

  it('shows the muted description of 128 and nothinking variants', async () => {
    const user = userEvent.setup()
    renderGrid([
      model('gemini-3-pro-preview'),
      model('gemini-3-pro-preview-128-nothinking'),
    ])
    await user.click(
      screen.getByRole('button', { name: 'Expand gemini-3-pro-preview' })
    )
    expect(
      screen.getByText('思考预算 128（最低思考强度）；会思考，但不返回思考内容')
    ).toBeVisible()
    expect(
      screen.getByTitle('gemini-3-pro-preview-128-nothinking')
    ).toHaveTextContent('128 · nothinking')
  })

  it('omits the line header when the only line has no tag', async () => {
    const user = userEvent.setup()
    renderGrid([model('gpt-5.5'), model('gpt-5.5-thinking')])
    await user.click(screen.getByRole('button', { name: 'Expand gpt-5.5' }))
    const panel = screen.getByRole('region', { name: /^gpt-5\.5/ })
    expect(within(panel).queryByRole('group')).toBeNull()
    expect(within(panel).queryByRole('heading', { level: 4 })).toBeNull()
    expect(
      within(panel).getAllByRole('button', { name: /^Details gpt-5\.5/ })
    ).toHaveLength(2)
  })

  it('copies the full model name of a variant and opens details for that variant', async () => {
    const user = userEvent.setup()
    const onModelClick = renderGrid(claudeCatalog())
    await user.click(
      screen.getByRole('button', { name: 'Expand claude-opus-4-6' })
    )
    const name = '[正价kiro]claude-opus-4-6-thinking-high'
    await user.click(screen.getByRole('button', { name: `Copy ${name}` }))
    expect(await navigator.clipboard.readText()).toBe(name)
    expect(onModelClick).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: `Details ${name}` }))
    expect(onModelClick).toHaveBeenCalledExactlyOnceWith(name)
  })

  it('collapses expanded families when changing page', async () => {
    const user = userEvent.setup()
    const models = [
      ...claudeCatalog(),
      ...Array.from({ length: 20 }, (_, index) => model(`solo-${index}`)),
    ]
    renderGrid(models)
    await user.click(
      screen.getByRole('button', { name: 'Expand claude-opus-4-6' })
    )
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await user.click(screen.getByRole('button', { name: 'Previous page' }))
    expect(
      screen.getByRole('button', { name: 'Expand claude-opus-4-6' })
    ).toHaveAttribute('aria-expanded', 'false')
  })

  it('pages by family so 22 families take two pages', async () => {
    const user = userEvent.setup()
    renderGrid([
      ...claudeCatalog(),
      ...Array.from({ length: 20 }, (_, index) => model(`solo-${index}`)),
    ])
    expect(screen.getAllByRole('heading', { level: 3 })).toHaveLength(20)
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    expect(screen.getAllByRole('heading', { level: 3 })).toHaveLength(2)
  })

  it('keeps one card per model name when grouping is off', () => {
    const models = claudeCatalog()
    renderWithI18n(
      withQuery(<ModelCardGrid models={models} onModelClick={vi.fn()} />)
    )
    expect(screen.getAllByRole('heading', { level: 3 })).toHaveLength(
      models.length
    )
    expect(screen.queryByRole('button', { name: /^Expand/ })).toBeNull()
  })
})

describe('family table rows', () => {
  function renderTable(models: PricingModel[], onModelClick = vi.fn()) {
    renderWithI18n(
      <PricingTable
        models={models}
        families={groupModelFamilies(models, RULES)}
        onModelClick={onModelClick}
      />
    )
    return onModelClick
  }

  it('shows one parent row per family with its variant count and lowest prices', () => {
    renderTable(claudeCatalog())
    const parent = screen.getByRole('row', {
      name: /claude-opus-4-6\s*Variants: 8/,
    })
    expect(within(parent).getByText('from 0.9')).toBeVisible()
    expect(
      screen.queryByRole('row', { name: /\[按次\]claude-opus-4-6-thinking / })
    ).toBeNull()
    expect(
      screen.getByRole('row', {
        name: /\[正价kiro\]claude-sonnet-4-6-thinking-high/,
      })
    ).toBeVisible()
  })

  it('expands variant rows with full names and opens details for a clicked variant', async () => {
    const user = userEvent.setup()
    const onModelClick = renderTable(claudeCatalog())
    const toggle = screen.getByRole('button', {
      name: 'Expand claude-opus-4-6',
    })
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle).toHaveFocus()
    expect(onModelClick).not.toHaveBeenCalled()

    const variantRow = screen.getByRole('row', {
      name: /\[按次\]claude-opus-4-6-thinking-low/,
    })
    await user.click(variantRow)
    expect(onModelClick).toHaveBeenCalledExactlyOnceWith(
      '[按次]claude-opus-4-6-thinking-low'
    )
  })

  it('toggles a family when its parent row is clicked', async () => {
    const user = userEvent.setup()
    const onModelClick = renderTable(claudeCatalog())
    await user.click(
      screen.getByRole('row', { name: /claude-opus-4-6\s*Variants: 8/ })
    )
    expect(
      screen.getByRole('button', { name: 'Collapse claude-opus-4-6' })
    ).toHaveAttribute('aria-expanded', 'true')
    expect(onModelClick).not.toHaveBeenCalled()
  })

  it('pages by family even when a family is expanded', async () => {
    const user = userEvent.setup()
    renderTable([
      ...claudeCatalog(),
      ...Array.from({ length: 20 }, (_, index) => model(`solo-${index}`)),
    ])
    await user.click(
      screen.getByRole('button', { name: 'Expand claude-opus-4-6' })
    )
    // 20 top-level rows plus the 8 variant rows, plus the header row.
    expect(screen.getAllByRole('row')).toHaveLength(1 + 20 + 8)
  })
})

describe('toolbar count', () => {
  function toolbar(props: { filteredCount: number; familyCount?: number }) {
    return (
      <PricingToolbar
        {...props}
        totalCount={30}
        sortBy='name'
        onSortChange={vi.fn()}
        tokenUnit='M'
        onTokenUnitChange={vi.fn()}
        showRechargePrice={false}
        onRechargePriceChange={vi.fn()}
        viewMode='card'
        onViewModeChange={vi.fn()}
        quotaTypeFilter='all'
        endpointTypeFilter='all'
        vendorFilter='all'
        groupFilter='all'
        tagFilter='all'
        onQuotaTypeChange={vi.fn()}
        onEndpointTypeChange={vi.fn()}
        onVendorChange={vi.fn()}
        onGroupChange={vi.fn()}
        onTagChange={vi.fn()}
        vendors={[]}
        groups={[]}
        tags={[]}
        models={[]}
        hasActiveFilters={false}
        activeFilterCount={0}
        onClearFilters={vi.fn()}
      />
    )
  }

  it('shows families and variants when grouping folded some names together', () => {
    renderWithI18n(toolbar({ filteredCount: 12, familyCount: 3 }))
    expect(screen.getByText('3').parentElement).toHaveTextContent(
      /^3\s*models\s*·\s*12\s*variants\s*\/ 30$/
    )
  })

  it('keeps the plain model count when every family has one model', () => {
    renderWithI18n(toolbar({ filteredCount: 12, familyCount: 12 }))
    expect(screen.getByText('12').parentElement).toHaveTextContent(
      /^12\s*models\s*\/ 30$/
    )
  })
})
