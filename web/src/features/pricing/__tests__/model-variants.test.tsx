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
import { filterBySearch, filterByVendor } from '../lib/filters'
import { pickBestModelPerf } from '../lib/model-family'
import {
  buildModelSquareIndex,
  DEFAULT_FILTER_SECTIONS,
  DEFAULT_MODEL_VARIANT_RULES,
  groupModelFamilies,
  parseModelName,
  resolveModelSquareSettings,
  splitFamilyBases,
  stripLineTag,
  validateFamilyBases,
  validateVariantRules,
  type ModelFamily,
  type ModelSquareSettings,
  type ModelVariantRule,
} from '../lib/model-variants'
import type { PricingModel } from '../types'
import {
  ALSO_CASES,
  BASE_CASES,
  BASE_SWITCH_CASES,
  FOLD_PARSE,
  LATEST_CASES,
  MUST_NOT_JOIN,
  ORDER_CASES,
  PARITY_NAMES,
  SCREENSHOT_CARDS,
  SCREENSHOT_KIRO_CARDS,
  SCREENSHOT_OPENAI_CARDS,
  SCREENSHOT_PLUS_OPUS_CARD,
  SCREENSHOT_SEARCH_CARDS,
  SCREENSHOT_UNFOLDED_CARDS,
  TITLE_CASES,
} from './model-square-fixtures'
import {
  familiesOf,
  fixtureVendor,
  LEGACY,
  SCREENSHOT_CATALOGUE,
  squareModel,
  squareModels,
  squareSettings,
} from './model-square-test-utils'

const RULES = DEFAULT_MODEL_VARIANT_RULES

/** Parse without the folds, as the grouping did before them. */
function legacyParse(
  name: string,
  rules: readonly ModelVariantRule[] = RULES,
  bases: readonly string[] = []
) {
  return parseModelName(name, {
    rules,
    familyBases: new Set(bases),
    versionSpellingFold: false,
    snapshotFold: false,
  })
}

/** Group without the folds, as the grouping did before them. */
function legacyFamilies(
  models: PricingModel[],
  rules: readonly ModelVariantRule[] = RULES,
  bases: string[] = []
) {
  return familiesOf(models, {
    ...LEGACY,
    variant_rules: [...rules],
    family_bases: bases,
  })
}

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
      key: 'claude-opus-4-6',
      suffixes: ['-thinking-high'],
      label: 'thinking-high',
    },
    {
      name: '[按次]gemini-3-pro-preview-128-nothinking',
      line: '按次',
      key: 'gemini-3-pro-preview',
      suffixes: ['-128', '-nothinking'],
      label: '128 · nothinking',
    },
    {
      name: '[按量]claude-fable-5-thinking',
      line: '按量',
      key: 'claude-fable-5',
      suffixes: ['-thinking'],
      label: 'thinking',
    },
    {
      name: 'gemini-3.1-pro-preview',
      line: '',
      key: 'gemini-3.1-pro-preview',
      suffixes: [],
      label: '',
    },
  ])(
    'splits $name into line "$line", key $key and label "$label"',
    (expected) => {
      const parsed = legacyParse(expected.name)
      expect(parsed).toMatchObject(expected)
    }
  )

  it('joins the descriptions of stacked -128 and -nothinking suffixes in name order', () => {
    expect(
      legacyParse('[按次]gemini-3-pro-preview-128-nothinking').description
    ).toBe('思考预算 128（最低思考强度）；会思考，但不返回思考内容')
  })

  it('prefers the longest matching suffix so -thinking-high is not read as -high', () => {
    const parsed = legacyParse('claude-opus-4-6-thinking-high')
    expect(parsed.ruleIndexes).toEqual([
      RULES.findIndex((rule) => rule.suffix === '-thinking-high'),
    ])
  })

  it.each(['-thinking', 'model--thinking'])(
    'keeps %s whole when stripping would leave an empty base or one ending in a dash',
    (name) => {
      expect(legacyParse(name)).toMatchObject({
        key: name,
        suffixes: [],
      })
    }
  )
})

describe('family bases', () => {
  const rules = DEFAULT_MODEL_VARIANT_RULES

  it('folds a suffix no rule covers into a configured base', () => {
    const parsed = legacyParse('gemini-4-argon-low', rules, ['gemini-4-argon'])
    expect(parsed).toMatchObject({
      key: 'gemini-4-argon',
      line: '',
      extra: 'low',
      label: 'low',
    })
  })

  it('keeps the line tag and stacks rule suffixes after the free-form part', () => {
    const parsed = legacyParse(
      '[按次]gemini-4-argon-medium-nothinking',
      rules,
      ['gemini-4-argon']
    )
    expect(parsed).toMatchObject({
      key: 'gemini-4-argon',
      line: '按次',
      label: 'medium · nothinking',
      description: '会思考，但不返回思考内容',
    })
  })

  it('never strips a rule suffix into the base', () => {
    expect(
      legacyParse('foo-thinking-high', rules, ['foo-thinking'])
    ).toMatchObject({ key: 'foo-thinking', label: 'high' })
    expect(legacyParse('foo-thinking', rules, ['foo-thinking'])).toMatchObject({
      key: 'foo-thinking',
      label: '',
    })
  })

  it('prefers the longest matching base', () => {
    const bases = ['gemini-4', 'gemini-4-argon']
    expect(legacyParse('gemini-4-argon-low', rules, bases)).toMatchObject({
      key: 'gemini-4-argon',
      label: 'low',
    })
    expect(legacyParse('gemini-4-pro', rules, bases)).toMatchObject({
      key: 'gemini-4',
      label: 'pro',
    })
  })

  it('ignores a base that the name only shares letters with', () => {
    expect(
      legacyParse('gemini-4-argonx-low', rules, ['gemini-4-argon'])
    ).toMatchObject({ key: 'gemini-4-argonx-low', label: '' })
  })

  it('shows every line of a base in one family ordered standard, low, medium, others', () => {
    const names = [
      '[按次]gemini-4-argon-zeta',
      '[按次]gemini-4-argon-medium',
      '[按次]gemini-4-argon-max',
      '[按次]gemini-4-argon-low',
      '[按次]gemini-4-argon',
      '[按量]gemini-4-argon-low',
      '[按量]gemini-4-argon',
    ]
    const families = legacyFamilies(
      names.map((name) => model(name)),
      rules,
      ['gemini-4-argon']
    )
    expect(families).toHaveLength(1)
    expect(families[0].title).toBe('gemini-4-argon')
    expect(
      families[0].lines.map((line) => [
        line.line,
        line.variants.map((variant) => variant.parsed.label),
      ])
    ).toEqual([
      ['按次', ['', 'low', 'medium', 'max', 'zeta']],
      ['按量', ['', 'low']],
    ])
  })

  it('splits one base per line, trimming and dropping blanks and repeats', () => {
    expect(
      splitFamilyBases(' gemini-4-argon \r\n\n gemini-4\ngemini-4-argon\n')
    ).toEqual(['gemini-4-argon', 'gemini-4'])
  })

  it.each([
    ['gemini 4', '{{base}}: must not contain spaces'],
    ['[按次]gemini-4', '{{base}}: leave out the leading [...] line tag'],
    ['gemini-4-', '{{base}}: must not end with -'],
    ['a'.repeat(129), '{{base}}: must be {{max}} characters or fewer'],
  ])('rejects the base "%s" like the backend', (base, message) => {
    expect(validateFamilyBases([base])).toEqual([
      expect.objectContaining({ index: 0, message }),
    ])
  })

  it('accepts up to 500 bases', () => {
    const bases = Array.from({ length: 500 }, (_, index) => `m-${index}`)
    expect(validateFamilyBases(bases)).toEqual([])
    expect(validateFamilyBases([...bases, 'one-more'])).toEqual([
      expect.objectContaining({
        index: -1,
        message: 'At most {{max}} base models',
      }),
    ])
  })

  it('counts the 128-character limit in code points like Go', () => {
    // Two UTF-16 units each: a length check in units would reject 128 of them.
    const astral = '\u{1F9E0}'
    expect(validateFamilyBases([astral.repeat(128)])).toEqual([])
    expect(validateFamilyBases([astral.repeat(129)])).toEqual([
      expect.objectContaining({
        message: '{{base}}: must be {{max}} characters or fewer',
      }),
    ])
  })
})

describe('groupModelFamilies', () => {
  it('puts every line of a base model into one family ordered 按次, 按量, then other lines', () => {
    const [opus] = legacyFamilies(claudeCatalog())
    expect(opus.title).toBe('claude-opus-4-6')
    expect(opus.lines.map((line) => line.line)).toEqual([
      '按次',
      '按量',
      '正价kiro',
    ])
    expect(opus.variants).toHaveLength(8)
  })

  it('orders variants inside a line by rule order with the standard variant first', () => {
    const [opus] = legacyFamilies(claudeCatalog())
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
    const [gemini] = legacyFamilies([
      model('[按次]gemini-3-pro-preview-128-nothinking'),
      model('[按次]gemini-3-pro-preview-nothinking'),
      model('[按次]gemini-3-pro-preview-128'),
      model('[按次]gemini-3-pro-preview'),
    ])
    expect(gemini.variantLabels).toEqual([
      '',
      'nothinking',
      '128',
      '128 · nothinking',
    ])
  })

  it('lists the untagged line before tagged lines', () => {
    const [family] = legacyFamilies([
      model('[按次]gpt-5.5'),
      model('gpt-5.5-thinking'),
    ])
    expect(family.lines.map((line) => line.line)).toEqual(['', '按次'])
  })

  it.each([
    ['gpt-5.1-codex', 'gpt-5.1-codex-max'],
    ['qwen', 'qwen-max'],
    ['o4-mini', 'o4-mini-high'],
  ])(
    'keeps %s and %s apart because the built-in rules fold no generic effort suffix',
    (plain, effort) => {
      const families = legacyFamilies([model(plain), model(effort)])
      expect(families.map((family) => family.title)).toEqual([plain, effort])
    }
  )

  it('keeps families in the order their first member appears in the sorted list', () => {
    const families = legacyFamilies([
      model('[按次]gemini-3-pro-preview-128'),
      model('[按次]claude-opus-4-6'),
      model('[按次]gemini-3-pro-preview'),
    ])
    expect(families.map((family) => family.title)).toEqual([
      'gemini-3-pro-preview',
      'claude-opus-4-6',
    ])
  })

  it('leaves a base model with one name as a single-member family', () => {
    const families = legacyFamilies(claudeCatalog())
    const sonnet = families.find((f) => f.key === 'claude-sonnet-4-6')
    expect(sonnet?.models.map((m) => m.model_name)).toEqual([
      '[正价kiro]claude-sonnet-4-6-thinking-high',
    ])
  })

  it('still groups the lines of one base model when the rule list is empty', () => {
    const families = legacyFamilies(claudeCatalog(), [])
    expect(
      families.map((family) => family.models.map((m) => m.model_name))
    ).toContainEqual([
      '[按次]claude-opus-4-6',
      '[按量]claude-opus-4-6',
      '[正价kiro]claude-opus-4-6',
    ])
    expect(
      families
        .find((family) => family.key === 'claude-opus-4-6-thinking-high')
        ?.lines.map((line) => line.line)
    ).toEqual(['按次', '正价kiro'])
  })
})

describe('resolveModelSquareSettings', () => {
  it('enables grouping with the built-in rules when the backend omits model_square', () => {
    expect(resolveModelSquareSettings(undefined)).toEqual({
      variant_grouping_enabled: true,
      version_spelling_fold_enabled: true,
      snapshot_fold_enabled: true,
      variant_rules: [...DEFAULT_MODEL_VARIANT_RULES],
      family_bases: [],
      filter_sections: {
        groups: true,
        vendors: true,
        tags: false,
        quota_types: true,
        endpoint_types: false,
      },
    })
  })

  it('reads the family bases and ignores a malformed list', () => {
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
        family_bases: ['gemini-4-argon'],
      }).family_bases
    ).toEqual(['gemini-4-argon'])
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
        family_bases: [1, 'gemini-4-argon'],
      }).family_bases
    ).toEqual([])
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

describe('pickBestModelPerf', () => {
  const hour = 1_800_000_000 - (1_800_000_000 % 3600)

  it('shows the variant with the highest success rate, not an average', () => {
    const best = {
      success_rate: 99.5,
      avg_latency_ms: 9000,
      avg_tps: 12,
      window_start: hour,
      recent_success_series: [{ ts: hour, success_rate: 100 }],
    }
    const perf = pickBestModelPerf([
      { success_rate: 60, avg_latency_ms: 1000, avg_tps: 40 },
      undefined,
      best,
      { success_rate: 84.5, avg_latency_ms: 2000, avg_tps: 30 },
    ])
    // Rate, status bar, latency and throughput all come from that variant.
    expect(perf).toBe(best)
  })

  it('breaks ties by request count, then by lower latency', () => {
    const busy = {
      success_rate: 100,
      avg_latency_ms: 5000,
      avg_tps: 1,
      request_count: 9,
    }
    const quiet = {
      success_rate: 100,
      avg_latency_ms: 100,
      avg_tps: 1,
      request_count: 2,
    }
    expect(pickBestModelPerf([quiet, busy])).toBe(busy)

    const slow = { success_rate: 100, avg_latency_ms: 5000, avg_tps: 1 }
    const fast = { success_rate: 100, avg_latency_ms: 800, avg_tps: 1 }
    expect(pickBestModelPerf([slow, fast])).toBe(fast)
  })

  it('prefers any variant with a success rate over one without', () => {
    const unrated = {
      success_rate: Number.NaN,
      avg_latency_ms: 100,
      avg_tps: 1,
    }
    const rated = { success_rate: 40, avg_latency_ms: 9000, avg_tps: 1 }
    expect(pickBestModelPerf([unrated, rated])).toBe(rated)
  })

  it('returns no metrics when no variant has any', () => {
    expect(pickBestModelPerf([undefined, undefined])).toBeUndefined()
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
          families={legacyFamilies(models)}
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
      expect.stringContaining('Standard[正价kiro]claude-opus-4-6$0.2/ request'),
      expect.stringContaining(
        'thinking-high[正价kiro]claude-opus-4-6-thinking-high$0.25/ request'
      ),
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
        families={legacyFamilies(models)}
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

// ----------------------------------------------------------------------------
// Version spellings and dated snapshots. The expected values (fixtures file)
// were computed by the reference implementation of the Model Square spec on
// these exact names.
// ----------------------------------------------------------------------------

describe('parseModelName with both folds', () => {
  const index = buildModelSquareIndex(
    squareModels(FOLD_PARSE.map(([name]) => name)),
    squareSettings()
  )

  it.each(FOLD_PARSE)(
    '%s: line "%s", spelling %s, key %s, label "%s"',
    (name, line, spelling, key, label) => {
      expect(index.byName.get(name)).toMatchObject({
        line,
        spelling,
        key,
        label,
      })
    }
  )

  it('keeps the date of a name as its snapshot, the dash date before a Vertex @NNN', () => {
    expect(index.byName.get('vertex_ai/mistral-small-2503@001')?.date).toEqual({
      format: 'YYMM',
      label: '2503',
      sortKey: [2, 2025, 3, 0],
    })
    expect(index.byName.get('text-bison@002')?.date?.format).toBe('@NNN')
    expect(index.byName.get('claude-opus-4-5')?.date).toBeUndefined()
  })

  it('matches suffix rules case-insensitively while the version fold is on', () => {
    const options = {
      rules: RULES,
      versionSpellingFold: true,
      snapshotFold: true,
    }
    expect(parseModelName('Claude-Opus-4-5-THINKING', options)).toMatchObject({
      key: 'claude-opus-4.5',
      spelling: 'Claude-Opus-4-5',
      suffixes: ['-THINKING'],
      label: 'thinking',
    })
    expect(
      parseModelName('Claude-Opus-4-5-THINKING', {
        ...options,
        versionSpellingFold: false,
      })
    ).toMatchObject({ key: 'Claude-Opus-4-5-THINKING', label: '' })
  })

  it('searches the folded spelling, the [line] in lower case and the peeled tail', () => {
    expect(
      index.byName.get('[正价kiro]claude-opus-4.5-thinking-max')
    ).toMatchObject({
      foldedName: 'claude-opus-4.5-thinking-max',
      searchText: '[正价kiro]claude-opus-4.5-thinking-max',
    })
    expect(index.byName.get('claude-opus-4-5-20251101')).toMatchObject({
      foldedName: 'claude-opus-4-5-20251101',
      searchText: 'claude-opus-4.5-20251101',
    })
  })
})

describe('-latest aliases (two passes over the catalogue)', () => {
  it.each(LATEST_CASES)(
    'in %j, %s gets key %s',
    (catalogue, name, key, label) => {
      const index = buildModelSquareIndex(
        squareModels(catalogue),
        squareSettings()
      )
      expect(index.byName.get(name)).toMatchObject({ key, label })
    }
  )

  it('never folds -latest in a single parse (no catalogue)', () => {
    expect(
      parseModelName('claude-3-5-sonnet-latest', {
        rules: RULES,
        versionSpellingFold: true,
        snapshotFold: true,
      }).key
    ).toBe('claude-3.5-sonnet-latest')
  })
})

describe('names that must not join', () => {
  it.each(MUST_NOT_JOIN)('keeps %s and %s apart', (a, b) => {
    const index = buildModelSquareIndex(squareModels([a, b]), squareSettings())
    expect(index.byName.get(a)?.key).not.toBe(index.byName.get(b)?.key)
  })
})

describe('family bases with the folds', () => {
  it.each(BASE_CASES)('bases %j fold their names', (bases, rows) => {
    const index = buildModelSquareIndex(
      squareModels(rows.map(([name]) => name)),
      squareSettings({ family_bases: bases })
    )
    expect(
      rows.map(([name]) => {
        const parsed = index.byName.get(name)
        return [name, parsed?.key, parsed?.label]
      })
    ).toEqual(rows)
  })

  it('compares bases in either spelling and never cuts an exact base', () => {
    const keysAndLabels = (bases: string[], names: string[]) => {
      const index = buildModelSquareIndex(
        squareModels(names),
        squareSettings({ family_bases: bases })
      )
      return names.map((name) => [
        name,
        index.byName.get(name)?.key,
        index.byName.get(name)?.label,
      ])
    }
    expect(
      keysAndLabels(
        ['claude-opus-4-5', 'gemini-4-argon'],
        [
          'claude-opus-4.5-thinking-max',
          'claude-opus-4-5-20251101-thinking',
          'gemini-4-argon-medium-nothinking',
          'claude-opus-4-5',
          'Claude-Opus-4.5-thinking',
        ]
      )
    ).toEqual([
      ['claude-opus-4.5-thinking-max', 'claude-opus-4.5', 'thinking-max'],
      [
        'claude-opus-4-5-20251101-thinking',
        'claude-opus-4.5',
        '20251101 · thinking',
      ],
      [
        'gemini-4-argon-medium-nothinking',
        'gemini-4-argon',
        'medium · nothinking',
      ],
      ['claude-opus-4-5', 'claude-opus-4.5', ''],
      ['Claude-Opus-4.5-thinking', 'claude-opus-4.5', 'thinking'],
    ])
    expect(
      keysAndLabels(
        ['kimi-k2-thinking'],
        ['kimi-k2-thinking', 'kimi-k2-thinking-251104', 'kimi-k2']
      )
    ).toEqual([
      ['kimi-k2-thinking', 'kimi-k2-thinking', ''],
      ['kimi-k2-thinking-251104', 'kimi-k2-thinking', '251104'],
      ['kimi-k2', 'kimi-k2', ''],
    ])
  })

  it('does not let base claude-opus-4 swallow claude-opus-4-5', () => {
    const index = buildModelSquareIndex(
      squareModels(['claude-opus-4-5', 'claude-opus-4']),
      squareSettings({ family_bases: ['claude-opus-4'] })
    )
    expect(index.byName.get('claude-opus-4-5')?.key).toBe('claude-opus-4.5')
    expect(index.byName.get('claude-opus-4')?.key).toBe('claude-opus-4')
  })

  it('keeps a dated name under its base as the extra while the snapshot fold is off', () => {
    const index = buildModelSquareIndex(
      squareModels(['claude-haiku-4-5-20251001']),
      squareSettings({
        family_bases: ['claude-haiku-4-5'],
        snapshot_fold_enabled: false,
      })
    )
    expect(index.byName.get('claude-haiku-4-5-20251001')).toMatchObject({
      key: 'claude-haiku-4.5',
      label: '20251001',
      extra: '20251001',
    })
  })

  it('keeps the version before a date as written in the key while the snapshot fold is off', () => {
    // The documented limit: two cards, while the search text of both reads
    // the dotted version, so search finds both spellings.
    const families = familiesOf(
      squareModels(['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101']),
      { snapshot_fold_enabled: false }
    )
    expect(families.map((family) => family.key)).toEqual([
      'claude-opus-4-5-20251101',
      'claude-opus-4.5-20251101',
    ])
    expect(
      families.map((family) => family.variants[0].parsed.searchText)
    ).toEqual(['claude-opus-4.5-20251101', 'claude-opus-4.5-20251101'])
  })

  it.each(BASE_SWITCH_CASES)(
    'base %j gives one card with every switch combination',
    (bases, names, combinations) => {
      for (const [version, snapshot, key, labels] of combinations) {
        const families = familiesOf(squareModels(names), {
          family_bases: bases,
          version_spelling_fold_enabled: version,
          snapshot_fold_enabled: snapshot,
        })
        expect(families).toHaveLength(1)
        expect(families[0].key).toBe(key)
        expect(
          names.map(
            (name) =>
              families[0].variants.find(
                (variant) => variant.model.model_name === name
              )?.parsed.label
          )
        ).toEqual(labels)
      }
    }
  )
})

describe('card titles', () => {
  it.each(TITLE_CASES)(
    '%j is titled %s',
    (names, title, fullName, alsoWrittenAs, lead, chips) => {
      const families = familiesOf(squareModels(names))
      expect(families).toHaveLength(1)
      expect(families[0]).toMatchObject({
        title,
        titleIsFullName: fullName,
        alsoWrittenAs,
        variantLabels: chips,
      })
      expect(families[0].lead.model_name).toBe(lead)
      expect(families[0].models).toHaveLength(names.length)
    }
  )

  it.each(ALSO_CASES)('%j names only real other spellings', (names, cards) => {
    expect(
      familiesOf(squareModels(names)).map((family) => [
        family.title,
        family.alsoWrittenAs,
        family.models.length,
      ])
    ).toEqual(cards)
  })

  it('prefers the dashed spelling for dash-version vendors and the dotted one otherwise', () => {
    const titleWith = (vendor?: string) =>
      familiesOf([
        squareModel('foo-2-1', { vendor_name: vendor }),
        squareModel('foo-2.1', { vendor_name: vendor }),
      ])[0].title
    expect(titleWith('Anthropic')).toBe('foo-2-1')
    // The names the official metadata sync saves for ByteDance Ark.
    expect(titleWith('Volcengine Ark')).toBe('foo-2-1')
    expect(titleWith('Volcengine Ark Coding Plan')).toBe('foo-2-1')
    expect(titleWith('OpenAI')).toBe('foo-2.1')
    expect(titleWith(undefined)).toBe('foo-2.1')
  })

  it('titles synced Volcengine Ark members with the dashed ByteDance spelling', () => {
    const [family] = familiesOf([
      squareModel('doubao-seed-2-1-pro', { vendor_name: '字节跳动' }),
      squareModel('doubao-seed-2.1-pro', {
        vendor_name: 'Volcengine Ark Coding Plan',
      }),
      squareModel('doubao-seed-2-1-pro-260628', {
        vendor_name: 'Volcengine Ark',
      }),
      squareModel('doubao-seed-2.1-pro-thinking', {
        vendor_name: 'Volcengine Ark',
      }),
    ])
    expect(family.title).toBe('doubao-seed-2-1-pro')
    expect(family.vendor?.name).toBe('字节跳动')
    expect(family.alsoWrittenAs).toEqual(['doubao-seed-2.1-pro'])
  })
})

describe('variant order', () => {
  it.each(ORDER_CASES)(
    '%j: named variants, then latest, then snapshots newest first',
    (names, chips, rows) => {
      const [family] = familiesOf(squareModels(names))
      expect(family.variantLabels).toEqual(chips)
      expect(
        family.variants.map((variant) => variant.model.model_name)
      ).toEqual(rows)
    }
  )

  it('compares in code-point order, not by the browser locale', () => {
    const [family] = familiesOf(
      squareModels(['[b]m-1', '[B]m-1', '[a]m-1', '[按次]m-1', 'm-1'])
    )
    expect(family.lines.map((line) => line.line)).toEqual([
      '',
      '按次',
      'B',
      'a',
      'b',
    ])
  })
})

function screenshotFamilies(
  names: readonly string[] = SCREENSHOT_CATALOGUE,
  overrides: Partial<ModelSquareSettings> = {}
) {
  return familiesOf(squareModels(names), overrides)
}

/** A family as the fixture tables write it (FamilyRow). */
function describeFamily(family: ModelFamily) {
  return [
    family.key,
    family.models.length > 1
      ? family.title
      : stripLineTag(family.models[0].model_name),
    family.models.length,
    family.alsoWrittenAs,
    family.vendor?.name,
    family.lead.model_name,
    family.variantLabels,
    family.lines.map((line) => [
      line.line,
      line.variants.map((variant) => [
        variant.model.model_name,
        variant.parsed.label,
      ]),
    ]),
  ]
}

describe('the screenshot catalogue', () => {
  it('folds the 27 names into 12 cards', () => {
    expect(screenshotFamilies().map(describeFamily)).toEqual(SCREENSHOT_CARDS)
  })

  it('shows every name on its own card again with both folds off', () => {
    expect(
      screenshotFamilies(SCREENSHOT_CATALOGUE, LEGACY).map((family) => [
        family.models.length > 1
          ? family.title
          : stripLineTag(family.models[0].model_name),
        family.models.length,
      ])
    ).toEqual(SCREENSHOT_UNFOLDED_CARDS)
  })

  it.each(SCREENSHOT_SEARCH_CARDS)(
    'keeps the titles while the search %s shows only some members',
    (query, cards) => {
      const catalogue = squareModels(SCREENSHOT_CATALOGUE)
      const index = buildModelSquareIndex(catalogue, squareSettings())
      expect(
        groupModelFamilies(filterBySearch(catalogue, query, index), index).map(
          describeFamily
        )
      ).toEqual(cards)
    }
  )

  it('keeps the title and the vendor while only the Kiro members are shown', () => {
    const catalogue = squareModels(SCREENSHOT_CATALOGUE)
    const index = buildModelSquareIndex(catalogue, squareSettings())
    const kiro = catalogue.filter((model) =>
      model.model_name.startsWith('[正价kiro]claude-opus-4.6')
    )
    expect(groupModelFamilies(kiro, index).map(describeFamily)).toEqual(
      SCREENSHOT_KIRO_CARDS
    )
  })

  it('lists merged families under their vendor', () => {
    const catalogue = squareModels(SCREENSHOT_CATALOGUE)
    const index = buildModelSquareIndex(catalogue, squareSettings())
    const openai = filterByVendor(catalogue, 'OpenAI')
    expect(groupModelFamilies(openai, index).map(describeFamily)).toEqual(
      SCREENSHOT_OPENAI_CARDS
    )
  })

  it('switches the title to the dotted spelling once it has the most members', () => {
    const [opus] = screenshotFamilies([
      ...SCREENSHOT_CATALOGUE,
      'claude-opus-4.6',
    ])
    expect(describeFamily(opus)).toEqual(SCREENSHOT_PLUS_OPUS_CARD)
  })

  it('puts the chip of a full-name title first', () => {
    expect(
      familiesOf(squareModels(['x-thinking', 'x-20250101']))[0].variantLabels
    ).toEqual(['20250101', 'thinking'])
  })
})

describe('family vendor', () => {
  const vendorCatalogue = () => [
    squareModel('tts-1', { vendor_name: 'OpenAI', vendor_icon: 'OpenAI' }),
    squareModel('tts-1-1106', { vendor_name: 'OpenAI', vendor_icon: 'OpenAI' }),
    squareModel('[正价kiro]tts-1', {
      vendor_name: 'Kiro',
      vendor_icon: 'Kiro',
    }),
  ]

  it('is the vendor of the lead, and the filtered vendor under a vendor filter', () => {
    const catalogue = vendorCatalogue()
    const index = buildModelSquareIndex(catalogue, squareSettings())
    const [all] = groupModelFamilies(catalogue, index)
    const [kiro] = groupModelFamilies(filterByVendor(catalogue, 'Kiro'), index)
    expect([all.vendor, all.title]).toEqual([
      { name: 'OpenAI', icon: 'OpenAI' },
      'tts-1',
    ])
    expect([kiro.vendor, kiro.title, kiro.lead.model_name]).toEqual([
      { name: 'Kiro', icon: 'Kiro' },
      'tts-1',
      '[正价kiro]tts-1',
    ])
  })

  it('falls back to the most common vendor of the shown members, with its icon', () => {
    const [family] = familiesOf([
      squareModel('tts-1', { vendor_name: undefined }),
      squareModel('tts-1-1106', { vendor_name: undefined }),
      squareModel('[按次]tts-1', {
        vendor_name: 'OpenAI',
        vendor_icon: 'OpenAI',
      }),
    ])
    expect(family.lead.model_name).toBe('tts-1')
    expect(family.vendor).toEqual({ name: 'OpenAI', icon: 'OpenAI' })
  })

  it('takes the earliest member on a tie', () => {
    const [family] = familiesOf([
      squareModel('[b]x-1', { vendor_name: 'B' }),
      squareModel('[a]x-1', { vendor_name: 'A' }),
      squareModel('x-1-thinking', { vendor_name: undefined }),
    ])
    expect(family.lead.model_name).toBe('[a]x-1')
    expect(family.vendor?.name).toBe('A')
    const [noLeadVendor] = familiesOf([
      squareModel('x-1', { vendor_name: undefined }),
      squareModel('[b]x-1', { vendor_name: 'B' }),
      squareModel('[a]x-1', { vendor_name: 'A' }),
    ])
    expect(noLeadVendor.vendor?.name).toBe('B')
  })
})

describe('family lead', () => {
  it('is the untagged copy of a variant, not a line-tagged one', () => {
    const [family] = familiesOf([
      squareModel('[按次]claude-opus-4-5-thinking', {
        description: 'per-call line',
      }),
      squareModel('claude-opus-4-5-thinking', {
        icon: 'Claude.Color',
        description: 'untagged line',
      }),
    ])
    expect(family).toMatchObject({
      title: 'claude-opus-4-5-thinking',
      titleIsFullName: true,
    })
    expect(family.lead).toMatchObject({
      model_name: 'claude-opus-4-5-thinking',
      icon: 'Claude.Color',
      description: 'untagged line',
    })
  })

  it('orders line copies of one variant by line, so a search leads with the untagged copy', () => {
    const catalogue = squareModels([
      'claude-opus-4-6',
      'claude-opus-4-6-thinking',
      '[按量]claude-opus-4-6',
      '[按量]claude-opus-4-6-thinking',
      '[官KEY]claude-opus-4-6',
      '[官KEY]claude-opus-4-6-thinking',
      '[正价kiro]claude-opus-4.6',
      '[正价kiro]claude-opus-4.6-thinking',
    ])
    const index = buildModelSquareIndex(catalogue, squareSettings())
    const [family] = groupModelFamilies(
      filterBySearch(catalogue, 'thinking', index),
      index
    )
    expect(family.models).toHaveLength(4)
    expect(family.title).toBe('claude-opus-4-6')
    expect(family.lead.model_name).toBe('claude-opus-4-6-thinking')
    expect(index.titles.get(family.key)?.order).toEqual([
      'claude-opus-4-6',
      '[按量]claude-opus-4-6',
      '[官KEY]claude-opus-4-6',
      'claude-opus-4-6-thinking',
      '[按量]claude-opus-4-6-thinking',
      '[官KEY]claude-opus-4-6-thinking',
      '[正价kiro]claude-opus-4.6',
      '[正价kiro]claude-opus-4.6-thinking',
    ])
  })
})

// ----------------------------------------------------------------------------
// Both folds off: exactly the grouping without folds
// ----------------------------------------------------------------------------

/** The parser before the folds, kept to pin that both switches off change nothing. */
function parseWithoutFolds(
  name: string,
  rules: readonly ModelVariantRule[],
  bases: ReadonlySet<string>
) {
  const tag = /^\[([^\]]+)\]/.exec(name)
  const line = tag ? tag[1] : ''
  let base = tag ? name.slice(tag[0].length) : name
  let familyBase: string | undefined
  if (bases.size > 0) {
    if (bases.has(base)) familyBase = base
    else {
      for (
        let dash = base.lastIndexOf('-');
        dash > 0;
        dash = base.lastIndexOf('-', dash - 1)
      ) {
        if (bases.has(base.slice(0, dash))) {
          familyBase = base.slice(0, dash)
          break
        }
      }
    }
  }
  const minLength = familyBase?.length ?? 0
  const stripped: number[] = []
  for (;;) {
    let match = -1
    for (let index = 0; index < rules.length; index++) {
      const suffix = rules[index].suffix
      if (
        suffix &&
        base.endsWith(suffix) &&
        base.length - suffix.length >= minLength &&
        (match < 0 || suffix.length > rules[match].suffix.length)
      ) {
        match = index
      }
    }
    if (match < 0) break
    const rest = base.slice(0, base.length - rules[match].suffix.length)
    if (!rest || rest.endsWith('-')) break
    stripped.unshift(match)
    base = rest
  }
  let extra = ''
  if (familyBase !== undefined) {
    extra = base.slice(familyBase.length + 1)
    base = familyBase
  }
  const labels = stripped.map((index) => rules[index].label)
  if (extra) labels.unshift(extra)
  return { line, key: base, label: labels.join(' · ') }
}

describe('both folds off', () => {
  it.each<[string, readonly ModelVariantRule[], string[]]>([
    ['the built-in rules', RULES, []],
    [
      'the built-in rules and base models',
      RULES,
      [
        'claude-opus-4',
        'claude-opus-4-5',
        'gemini-4',
        'gemini-2.5-flash',
        'deepseek-v3',
        'glm-4',
        'qwen3',
        'gpt-4o',
        'kimi-k2-thinking',
        'claude',
        'llama-3',
      ],
    ],
    [
      'extra rules (one without a dash) and base models',
      [
        ...RULES,
        { suffix: '-low', label: 'low' },
        { suffix: '-high', label: 'high' },
        { suffix: 'thinking', label: 'bare' },
      ],
      ['claude-opus-4', 'gpt-5.1-codex', 'gemini-3-pro-preview', 'claude'],
    ],
  ])('group every name as without the folds, with %s', (_, rules, bases) => {
    const index = buildModelSquareIndex(
      squareModels(PARITY_NAMES),
      squareSettings({
        ...LEGACY,
        variant_rules: [...rules],
        family_bases: bases,
      })
    )
    const baseSet = new Set(bases)
    for (const name of PARITY_NAMES) {
      const parsed = index.byName.get(name)
      expect([
        name,
        { line: parsed?.line, key: parsed?.key, label: parsed?.label },
      ]).toEqual([name, parseWithoutFolds(name, rules, baseSet)])
    }
  })

  it('keeps the names as written', () => {
    const index = buildModelSquareIndex(
      squareModels([
        'claude-opus-4-5',
        'gpt-4o-mini-2024-07-18',
        'DeepSeek-V3',
      ]),
      squareSettings(LEGACY)
    )
    expect(
      ['claude-opus-4-5', 'gpt-4o-mini-2024-07-18', 'DeepSeek-V3'].map(
        (name) => index.byName.get(name)?.key
      )
    ).toEqual(['claude-opus-4-5', 'gpt-4o-mini-2024-07-18', 'DeepSeek-V3'])
  })
})

// ----------------------------------------------------------------------------
// The screenshot catalogue on the page: cards and table rows
// ----------------------------------------------------------------------------

describe('screenshot catalogue cards and rows', () => {
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

  /** The catalogue with each vendor's icon, as the pricing payload sends it. */
  function catalogue(names: readonly string[] = SCREENSHOT_CATALOGUE) {
    return names.map((name) =>
      squareModel(name, { vendor_icon: fixtureVendor(name) })
    )
  }

  function renderCards(models: PricingModel[]) {
    renderWithI18n(
      <QueryClientProvider client={queryClient}>
        <ModelCardGrid
          models={models}
          families={familiesOf(models)}
          onModelClick={vi.fn()}
        />
      </QueryClientProvider>
    )
  }

  function cardTitled(title: string) {
    const card = screen
      .getByRole('heading', { level: 3, name: title })
      .closest<HTMLElement>('[data-slot=card]')
    if (!card) throw new Error(`No card for ${title}`)
    return card
  }

  /** The line under a card title (vendor icons may also carry the name). */
  function vendorLine(card: HTMLElement) {
    return within(card).getByRole('heading', { level: 3 }).nextElementSibling
  }

  it('shows 12 cards titled with a real spelling, the other spelling and every line', () => {
    renderCards(catalogue())
    expect(
      screen
        .getAllByRole('heading', { level: 3 })
        .map((heading) => heading.textContent)
    ).toEqual([
      'claude-opus-4-6',
      'claude-sonnet-4-6',
      'gpt-4o-mini',
      'tts-1',
      'tts-1-hd',
      'text-embedding-ada-002',
      'text-embedding-3-small',
      'deepseek-v4-flash',
      'deepseek-v4-pro',
      'claude-opus-4-5',
      'claude-haiku-4-5',
      '[按次]claude-haiku-4-6',
    ])
    const opus = cardTitled('claude-opus-4-6')
    expect(vendorLine(opus)).toHaveTextContent(/^Anthropic$/)
    expect(
      within(opus).getByText('Also written as claude-opus-4.6')
    ).toBeVisible()
    const lines = within(opus).getByRole('group', { name: 'Lines' })
    expect(
      within(lines)
        .getAllByRole('listitem')
        .map((item) => item.textContent)
    ).toEqual([
      'defaultfrom $1 / request',
      '按量from $1 / request',
      '官KEYfrom $1 / request',
      '正价kirofrom $1 / request',
    ])
    const variants = within(opus).getByRole('group', { name: 'Variants' })
    expect(
      within(variants)
        .getAllByRole('listitem')
        .map((item) => item.textContent)
    ).toEqual(['Standard', 'thinking', 'thinking-xhigh', 'thinking-max'])
    const haiku = cardTitled('claude-haiku-4-5')
    expect(within(haiku).getByText('20251001')).toBeVisible()
    expect(within(haiku).queryByText(/^Also written as/)).toBeNull()
  })

  it('keeps the count of further spellings out of the truncated names', () => {
    renderCards(
      catalogue([
        'deepseek-v3',
        'DeepSeek-V3',
        'Pro/deepseek-v3',
        'deepseek-ai/DeepSeek-V3',
      ])
    )
    const card = cardTitled('deepseek-v3')
    const names = within(card).getByText(
      'Also written as DeepSeek-V3, Pro/deepseek-v3'
    )
    expect(names).toHaveClass('truncate')
    const more = within(card).getByText('+1')
    expect(more).not.toHaveClass('truncate')
    expect(names).not.toContainElement(more)
    expect(names.parentElement).toContainElement(more)
    expect(names.parentElement).toHaveAttribute(
      'title',
      'Also written as DeepSeek-V3, Pro/deepseek-v3, deepseek-ai/DeepSeek-V3'
    )
  })

  it('shows the vendor icon of merged OpenAI cards instead of a letter', () => {
    renderCards(catalogue())
    const tts = cardTitled('tts-1')
    expect(vendorLine(tts)).toHaveTextContent(/^OpenAI$/)
    expect(within(tts).queryByText('T')).toBeNull()
    expect(within(tts).getByText('1106')).toBeVisible()
  })

  it('names the full model of every row, so two Standard rows of one line differ', async () => {
    const user = userEvent.setup()
    renderCards(catalogue([...SCREENSHOT_CATALOGUE, 'claude-opus-4.6']))
    const opus = cardTitled('claude-opus-4.6')
    expect(
      within(opus).getByText('Also written as claude-opus-4-6')
    ).toBeVisible()
    await user.click(
      within(opus).getByRole('button', { name: 'Expand claude-opus-4.6' })
    )
    const panel = screen.getByRole('region', {
      name: 'claude-opus-4.6 Variants: 9',
    })
    const [untagged] = within(panel).getAllByRole('group')
    expect(
      within(untagged)
        .getAllByRole('listitem')
        .map((row) => row.textContent)
    ).toEqual([
      expect.stringMatching(/^Standardclaude-opus-4-6\$1/),
      expect.stringMatching(/^Standardclaude-opus-4\.6\$1/),
      expect.stringMatching(/^thinkingclaude-opus-4-6-thinking\$1/),
    ])
  })

  it('shows the same families as table rows with the family vendor', async () => {
    const user = userEvent.setup()
    const models = catalogue()
    renderWithI18n(
      <PricingTable
        models={models}
        families={familiesOf(models)}
        onModelClick={vi.fn()}
      />
    )
    const opus = screen.getByRole('row', {
      name: /claude-opus-4-6\s*Variants: 8/,
    })
    const vendorCell = opus.querySelector('[data-column-id=vendor_name]')
    expect(vendorCell).toHaveTextContent('Anthropic')
    // Testing Library collapses the line break of the tooltip to a space.
    expect(
      within(opus).getByTitle('claude-opus-4-6 Also written as claude-opus-4.6')
    ).toHaveAttribute(
      'title',
      'claude-opus-4-6\nAlso written as claude-opus-4.6'
    )
    expect(
      screen.getByRole('row', { name: /\[按次\]claude-haiku-4-6/ })
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Expand deepseek-v4-flash' })
    )
    expect(
      screen.getByRole('row', { name: /\[按次\]deepseek-v4-flash-0731/ })
    ).toBeVisible()
    expect(
      screen.getByRole('row', { name: /\[按次\]deepseek-v4-flash\b(?!-)/ })
    ).toBeVisible()
  })
})

describe('resolveModelSquareSettings with the folds and filter sections', () => {
  it('reads the folds and keeps their defaults when missing or malformed', () => {
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
      })
    ).toMatchObject({
      version_spelling_fold_enabled: true,
      snapshot_fold_enabled: true,
      filter_sections: DEFAULT_FILTER_SECTIONS,
    })
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
        version_spelling_fold_enabled: false,
        snapshot_fold_enabled: 'false',
      })
    ).toMatchObject({
      version_spelling_fold_enabled: false,
      snapshot_fold_enabled: true,
    })
  })

  it('takes each filter section as sent and the default for the others', () => {
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
        filter_sections: { tags: true, vendors: 'no' },
      }).filter_sections
    ).toEqual({
      groups: true,
      vendors: true,
      tags: true,
      quota_types: true,
      endpoint_types: false,
    })
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
        filter_sections: {
          groups: false,
          vendors: false,
          tags: false,
          quota_types: false,
          endpoint_types: true,
        },
      }).filter_sections
    ).toEqual({
      groups: false,
      vendors: false,
      tags: false,
      quota_types: false,
      endpoint_types: true,
    })
    expect(
      resolveModelSquareSettings({
        variant_grouping_enabled: true,
        variant_rules: [],
        filter_sections: null,
      }).filter_sections
    ).toEqual(DEFAULT_FILTER_SECTIONS)
  })
})
