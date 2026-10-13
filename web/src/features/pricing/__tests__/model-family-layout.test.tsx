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
import {
  DEFAULT_MODEL_VARIANT_RULES,
  groupModelFamilies,
  type ModelFamily,
} from '../lib/model-variants'
import type { PricingModel } from '../types'

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({
    lng: 'en',
    resources: { en: { translation: {} } },
    interpolation: { escapeValue: false },
  })
})

let nextId = 1
function model(name: string): PricingModel {
  return {
    id: nextId++,
    model_name: name,
    quota_type: 1,
    model_ratio: 1,
    completion_ratio: 1,
    model_price: 1,
    enable_groups: ['default'],
    group_ratio: { default: 1 },
  }
}

/** Families a-1 … e-1 (as many as asked), each with a thinking variant. */
function familyModels(count: number): PricingModel[] {
  return ['a', 'b', 'c', 'd', 'e']
    .slice(0, count)
    .flatMap((letter) => [model(`${letter}-1`), model(`${letter}-1-thinking`)])
}

const originalMatchMedia = window.matchMedia
const originalScrollIntoView = HTMLElement.prototype.scrollIntoView

/** Answers the grid's `min-width` breakpoint queries for a viewport width. */
function mockViewportWidth(width: number) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string): MediaQueryList => {
      const minWidth = /min-width:\s*([\d.]+)rem/.exec(query)
      return {
        ...originalMatchMedia(query),
        matches: minWidth
          ? width >= Number(minWidth[1]) * 16
          : originalMatchMedia(query).matches,
      }
    },
  })
}

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
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: originalMatchMedia,
  })
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
    configurable: true,
    value: originalScrollIntoView,
  })
})

function gridNode(families: ModelFamily[], models: PricingModel[]) {
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <ModelCardGrid
          models={models}
          families={families}
          onModelClick={vi.fn()}
        />
      </QueryClientProvider>
    </I18nextProvider>
  )
}

function renderGrid(models: PricingModel[]) {
  const families = groupModelFamilies(models, DEFAULT_MODEL_VARIANT_RULES)
  const view = render(gridNode(families, models))
  return {
    rerenderWith: (next: ModelFamily[]) =>
      view.rerender(gridNode(next, models)),
  }
}

function familyCard(base: string): HTMLElement {
  const card = screen
    .getByRole('heading', { level: 3, name: base })
    .closest<HTMLElement>('[data-slot=card]')
  if (!card) throw new Error(`No card for ${base}`)
  return card
}

/** Grid items in DOM order: card titles, and `panel` for the variant panel. */
function gridOrder(): string[] {
  const grid = familyCard('a-1').parentElement
  if (!grid) throw new Error('No grid')
  return [...grid.children].map((item) =>
    item.getAttribute('role') === 'region'
      ? 'panel'
      : (within(item as HTMLElement).getByRole('heading', { level: 3 })
          .textContent ?? '')
  )
}

function toggleOf(base: string): HTMLElement {
  return within(familyCard(base)).getByRole('button', {
    name: new RegExp(`^(Expand|Collapse) ${base}$`),
  })
}

describe('variant panel placement', () => {
  it.each([
    { column: 2, base: 'b-1' },
    { column: 3, base: 'c-1' },
  ])(
    'puts the panel after the row and keeps the card in column $column of three',
    async ({ base }) => {
      mockViewportWidth(1280)
      const user = userEvent.setup()
      renderGrid(familyModels(4))

      await user.click(toggleOf(base))

      expect(gridOrder()).toEqual(['a-1', 'b-1', 'c-1', 'panel', 'd-1'])
      expect(familyCard(base)).toHaveAttribute('data-expanded', 'true')
      expect(familyCard(base)).not.toHaveClass('col-span-full')
      expect(screen.getByRole('region')).toHaveClass('col-span-full')
    }
  )

  it('puts the panel after a short last row with two columns', async () => {
    mockViewportWidth(1000)
    const user = userEvent.setup()
    renderGrid(familyModels(3))

    await user.click(toggleOf('c-1'))

    expect(gridOrder()).toEqual(['a-1', 'b-1', 'c-1', 'panel'])
  })

  it('puts the panel directly after the card on a one-column phone layout', async () => {
    mockViewportWidth(390)
    const user = userEvent.setup()
    renderGrid(familyModels(3))

    await user.click(toggleOf('b-1'))

    expect(gridOrder()).toEqual(['a-1', 'b-1', 'panel', 'c-1'])
  })

  it('opens one family at a time', async () => {
    mockViewportWidth(1280)
    const user = userEvent.setup()
    renderGrid(familyModels(4))

    await user.click(toggleOf('a-1'))
    await user.click(toggleOf('d-1'))

    expect(toggleOf('a-1')).toHaveAttribute('aria-expanded', 'false')
    expect(toggleOf('d-1')).toHaveAttribute('aria-expanded', 'true')
    expect(gridOrder()).toEqual(['a-1', 'b-1', 'c-1', 'd-1', 'panel'])
  })
})

describe('variant panel focus and scrolling', () => {
  it('moves focus into the panel so the next Tabs reach its controls and first variant', async () => {
    const user = userEvent.setup()
    renderGrid(familyModels(2))
    toggleOf('a-1').focus()

    await user.keyboard('{Enter}')

    const panel = screen.getByRole('region', { name: 'a-1 Variants: 2' })
    expect(toggleOf('a-1')).toHaveAttribute('aria-controls', panel.id)
    expect(within(panel).getByRole('heading', { level: 3 })).toHaveFocus()
    await user.tab()
    expect(
      within(panel).getByRole('button', { name: 'Collapse a-1' })
    ).toHaveFocus()
    await user.tab()
    expect(
      within(panel).getByRole('button', { name: 'Copy a-1' })
    ).toHaveFocus()
  })

  it('scrolls the panel into view below the fixed site header', async () => {
    const scrollIntoView = vi.fn()
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
      configurable: true,
      value: scrollIntoView,
    })
    const user = userEvent.setup()
    renderGrid(familyModels(2))

    await user.click(toggleOf('b-1'))

    const panel = screen.getByRole('region')
    expect(scrollIntoView).toHaveBeenCalledOnce()
    expect(scrollIntoView.mock.contexts[0]).toBe(panel)
    expect(scrollIntoView).toHaveBeenCalledWith({
      block: 'nearest',
      behavior: 'auto',
    })
    expect(panel).toHaveClass('scroll-mt-20')
  })

  it('returns focus to the card toggle when the panel is collapsed', async () => {
    const user = userEvent.setup()
    renderGrid(familyModels(2))
    await user.click(toggleOf('b-1'))

    await user.click(
      within(screen.getByRole('region')).getByRole('button', {
        name: 'Collapse b-1',
      })
    )

    expect(screen.queryByRole('region')).toBeNull()
    expect(toggleOf('b-1')).toHaveAttribute('aria-expanded', 'false')
    expect(toggleOf('b-1')).toHaveFocus()
  })
})

describe('variant panel reset', () => {
  it('closes the panel when the family list changes (search, filter or sort)', async () => {
    const user = userEvent.setup()
    const models = familyModels(2)
    const view = renderGrid(models)
    await user.click(toggleOf('a-1'))
    expect(screen.getByRole('region')).toBeVisible()

    view.rerenderWith(groupModelFamilies(models, DEFAULT_MODEL_VARIANT_RULES))

    expect(screen.queryByRole('region')).toBeNull()
    expect(toggleOf('a-1')).toHaveAttribute('aria-expanded', 'false')
  })
})
