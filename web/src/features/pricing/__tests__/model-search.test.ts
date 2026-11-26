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

import { filterAndSortModels, filterBySearch } from '../lib/filters'
import { buildModelSquareIndex } from '../lib/model-variants'
import {
  squareModel,
  squareModels,
  squareSettings,
} from './model-square-test-utils'

/** Dated names in both spellings, a tagged Kiro member and an org/ prefix. */
const SEARCH_CATALOGUE = [
  'claude-opus-4-5-20251101',
  'claude-haiku-4-5-20251001',
  'claude-sonnet-4-5-20250929',
  'doubao-seed-1-6-250615',
  'glm-4-7-251222',
  'claude-3-5-sonnet-20241022',
  'claude-opus-4.5',
  'claude-opus-4-5',
  'claude-opus-4.5-20251101',
  '[正价kiro]claude-opus-4.6',
  'claude-opus-4-6',
  'deepseek-ai/DeepSeek-V3',
  'deepseek-v3',
]

/** [query, the names it finds] with the version fold on (reference values). */
const SEARCHES: [string, string[]][] = [
  [
    'claude-opus-4.5-20251101',
    ['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101'],
  ],
  [
    'claude-opus-4-5-20251101',
    ['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101'],
  ],
  [
    'claude-opus-4-5-2025',
    ['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101'],
  ],
  ['claude-haiku-4.5-20251001', ['claude-haiku-4-5-20251001']],
  ['claude-sonnet-4.5-2025', ['claude-sonnet-4-5-20250929']],
  ['doubao-seed-1.6-250615', ['doubao-seed-1-6-250615']],
  ['glm-4.7-251222', ['glm-4-7-251222']],
  ['claude-3.5-sonnet-20241022', ['claude-3-5-sonnet-20241022']],
  [
    'claude-opus-4.5',
    [
      'claude-opus-4-5-20251101',
      'claude-opus-4.5',
      'claude-opus-4-5',
      'claude-opus-4.5-20251101',
    ],
  ],
  [
    'claude-opus-4.5-',
    ['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101'],
  ],
  [
    'claude-opus-4.5-2',
    ['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101'],
  ],
  ['[正价kiro]claude-opus-4-6', ['[正价kiro]claude-opus-4.6']],
  ['[正价KIRO]claude-opus-4.6', ['[正价kiro]claude-opus-4.6']],
  ['deepseek-ai/DeepSeek-V3', ['deepseek-ai/DeepSeek-V3', 'deepseek-v3']],
  ['DeepSeek-V3', ['deepseek-ai/DeepSeek-V3', 'deepseek-v3']],
  ['claude-opus-4-6', ['[正价kiro]claude-opus-4.6', 'claude-opus-4-6']],
]

/** The owner's fixture names (versions written as 4.x) as one catalogue. */
const OWNER_CATALOGUE = [
  'claude-opus-4-6',
  'claude-opus-4.6',
  'claude-opus-4.6-thinking',
  'claude-opus-4.6-thinking-xhigh',
  'claude-opus-4.6-thinking-max',
  'claude-opus-4-6-thinking',
  '[正价kiro]claude-opus-4.6',
  'claude-sonnet-4-6',
  'claude-sonnet-4.6',
  'gpt-4o-mini',
  'gpt-4o-mini-2024-07-18',
  'tts-1',
  'tts-1-1106',
  'tts-1-hd',
  'tts-1-hd-1106',
  'text-embedding-ada-002',
  'text-embedding-3-small',
  'deepseek-v4-flash',
  '[按次]deepseek-v4-flash',
  '[按次]deepseek-v4-flash-0731',
  'deepseek-v4-pro',
  'deepseek-v4-pro-0813',
  '[正价kiro]claude-opus-4-5',
  'claude-opus-4-5-20251101',
  'claude-haiku-4-5',
  'claude-haiku-4-5-20251001',
  '[按次]claude-haiku-4-6',
  '[按量]claude-opus-4-6',
  '[正价kiro]claude-opus-4.6-thinking-max',
  'claude-3-5-sonnet',
  'claude-3.5-sonnet',
  'doubao-1-5-pro',
  'doubao-1.5-pro',
  'glm-4-9b',
  'llama-3-8b',
  'deepseek-v4-flash-0731',
  'text-embedding-v3',
  'text-embedding-004',
]

const OWNER_SEARCHES: [string, string[]][] = [
  [
    'claude-opus-4-6',
    [
      'claude-opus-4-6',
      'claude-opus-4.6',
      'claude-opus-4.6-thinking',
      'claude-opus-4.6-thinking-xhigh',
      'claude-opus-4.6-thinking-max',
      'claude-opus-4-6-thinking',
      '[正价kiro]claude-opus-4.6',
      '[按量]claude-opus-4-6',
      '[正价kiro]claude-opus-4.6-thinking-max',
    ],
  ],
  [
    'claude-opus-4.6',
    [
      'claude-opus-4-6',
      'claude-opus-4.6',
      'claude-opus-4.6-thinking',
      'claude-opus-4.6-thinking-xhigh',
      'claude-opus-4.6-thinking-max',
      'claude-opus-4-6-thinking',
      '[正价kiro]claude-opus-4.6',
      '[按量]claude-opus-4-6',
      '[正价kiro]claude-opus-4.6-thinking-max',
    ],
  ],
  [
    'thinking',
    [
      'claude-opus-4.6-thinking',
      'claude-opus-4.6-thinking-xhigh',
      'claude-opus-4.6-thinking-max',
      'claude-opus-4-6-thinking',
      '[正价kiro]claude-opus-4.6-thinking-max',
    ],
  ],
  ['gpt-4o-mini-2024-07-18', ['gpt-4o-mini-2024-07-18']],
  [
    'claude-opus-4.5',
    ['[正价kiro]claude-opus-4-5', 'claude-opus-4-5-20251101'],
  ],
  ['claude-3.5-sonnet', ['claude-3-5-sonnet', 'claude-3.5-sonnet']],
  ['doubao-1-5-pro', ['doubao-1-5-pro', 'doubao-1.5-pro']],
]

function search(names: readonly string[], query: string, foldOn = true) {
  const models = squareModels(names)
  const index = buildModelSquareIndex(
    models,
    squareSettings({ version_spelling_fold_enabled: foldOn })
  )
  return filterBySearch(models, query, index).map((model) => model.model_name)
}

describe('folded search', () => {
  it.each(SEARCHES)('%s finds %j', (query, names) => {
    expect(search(SEARCH_CATALOGUE, query)).toEqual(names)
  })

  it.each(OWNER_SEARCHES)(
    'over the owner names, %s finds %j',
    (query, names) => {
      expect(search(OWNER_CATALOGUE, query)).toEqual(names)
    }
  )

  it('finds a family by either spelling with the same members', () => {
    const dashed = search(OWNER_CATALOGUE, 'claude-opus-4-6')
    const dotted = search(OWNER_CATALOGUE, 'claude-opus-4.6')
    expect(dashed).toContain('[正价kiro]claude-opus-4.6')
    expect(dotted).toContain('[按量]claude-opus-4-6')
    expect(dashed).toEqual(dotted)
  })

  it('matches only as written while the version fold is off', () => {
    expect(search(SEARCH_CATALOGUE, 'claude-opus-4.5', false)).toEqual([
      'claude-opus-4.5',
      'claude-opus-4.5-20251101',
    ])
    expect(search(SEARCH_CATALOGUE, 'claude-opus-4-5-2025', false)).toEqual([
      'claude-opus-4-5-20251101',
    ])
  })

  it('matches only as written without an index (grouping off)', () => {
    const models = squareModels(SEARCH_CATALOGUE)
    expect(
      filterBySearch(models, 'claude-opus-4.5').map((model) => model.model_name)
    ).toEqual(['claude-opus-4.5', 'claude-opus-4.5-20251101'])
  })

  it('still finds a model by description, tag or vendor', () => {
    const models = [
      squareModel('m-1', { description: 'Fast reasoning' }),
      squareModel('m-2', { tags: 'vision,cheap' }),
      squareModel('m-3', { vendor_name: 'Acme' }),
      squareModel('m-4'),
    ]
    const index = buildModelSquareIndex(models, squareSettings())
    const names = (query: string) =>
      filterBySearch(models, query, index).map((model) => model.model_name)
    expect(names('reasoning')).toEqual(['m-1'])
    expect(names('VISION')).toEqual(['m-2'])
    expect(names('acme')).toEqual(['m-3'])
  })

  it('passes the index through filterAndSortModels', () => {
    const models = squareModels(SEARCH_CATALOGUE)
    const index = buildModelSquareIndex(models, squareSettings())
    const filters = {
      search: 'claude-opus-4-5-2025',
      vendor: 'all',
      group: 'all',
      quotaType: 'all',
      endpointType: 'all',
      tag: 'all',
      sortBy: 'name',
    }
    expect(
      filterAndSortModels(models, filters, index).map(
        (model) => model.model_name
      )
    ).toEqual(['claude-opus-4-5-20251101', 'claude-opus-4.5-20251101'])
    expect(
      filterAndSortModels(models, filters).map((model) => model.model_name)
    ).toEqual(['claude-opus-4-5-20251101'])
  })
})
