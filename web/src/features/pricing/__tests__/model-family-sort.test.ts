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
import { describe, expect, it } from 'vitest'

import { SORT_OPTIONS } from '../constants'
import { sortModels } from '../lib/filters'
import { sortModelFamilies } from '../lib/model-family'
import {
  DEFAULT_MODEL_VARIANT_RULES,
  groupModelFamilies,
} from '../lib/model-variants'
import type { PricingModel } from '../types'

let nextId = 1
function model(name: string, price = 1): PricingModel {
  return {
    id: nextId++,
    model_name: name,
    quota_type: 1,
    model_ratio: 1,
    completion_ratio: 1,
    model_price: price,
    enable_groups: ['default'],
  }
}

/** Sort like the page does, group, then order the families. */
function familyTitles(models: PricingModel[], sortBy: string): string[] {
  const families = sortModelFamilies(
    groupModelFamilies(sortModels(models, sortBy), DEFAULT_MODEL_VARIANT_RULES),
    sortBy
  )
  return families.map((family) =>
    family.models.length === 1 ? family.models[0].model_name : family.base
  )
}

describe('sortModelFamilies', () => {
  it('orders families A-Z by base name with Name sort instead of restarting per line tag', () => {
    const titles = familyTitles(
      [
        model('[按次]claude-opus-4-6'),
        model('[按次]claude-opus-4-6-thinking'),
        model('[按次]zeta-1'),
        model('[按量]claude-fable-5'),
        model('[按量]claude-fable-5-thinking'),
        model('alpha-1'),
        model('gpt-5.5'),
      ],
      SORT_OPTIONS.NAME
    )
    expect(titles).toEqual([
      'alpha-1',
      'claude-fable-5',
      'claude-opus-4-6',
      'gpt-5.5',
      '[按次]zeta-1',
    ])
  })

  it('places a tagged single model by its name without the tag', () => {
    const titles = familyTitles(
      [
        model('[正价kiro]beta-2'),
        model('[按量]beta-10'),
        model('beta-1'),
        model('beta-1-thinking'),
      ],
      SORT_OPTIONS.NAME
    )
    expect(titles).toEqual(['beta-1', '[按量]beta-10', '[正价kiro]beta-2'])
  })

  it('ranks families by their lowest member price with Price: High to Low', () => {
    const titles = familyTitles(
      [
        model('[按次]alpha-1', 0.2),
        model('[按次]alpha-1-thinking-max', 5),
        model('[按次]beta-1', 1),
        model('[按次]gamma-1', 0.5),
      ],
      SORT_OPTIONS.PRICE_HIGH
    )
    expect(titles).toEqual(['[按次]beta-1', '[按次]gamma-1', 'alpha-1'])
  })

  it('keeps the order of the cheapest member with Price: Low to High', () => {
    const titles = familyTitles(
      [
        model('[按次]beta-1', 1),
        model('[按次]alpha-1-thinking-max', 5),
        model('[按次]alpha-1', 0.2),
      ],
      SORT_OPTIONS.PRICE_LOW
    )
    expect(titles).toEqual(['alpha-1', '[按次]beta-1'])
  })
})
