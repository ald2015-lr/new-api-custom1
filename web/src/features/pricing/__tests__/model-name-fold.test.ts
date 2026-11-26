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

import {
  dateSuffix,
  foldVersions,
  isVersionDash,
  orgPrefixLength,
  queryForms,
  snapshotSortKey,
  spellingKey,
  splitLineTag,
  type SnapshotFormat,
} from '../lib/model-name-fold'

describe('foldVersions', () => {
  it.each([
    ['claude-opus-4-5', 'claude-opus-4.5'],
    ['claude-3-5-sonnet', 'claude-3.5-sonnet'],
    ['doubao-1-5-pro', 'doubao-1.5-pro'],
    ['doubao-seed-1-6', 'doubao-seed-1.6'],
    // Never compared as numbers: 4.20 is not 4.2.
    ['grok-4-20-reasoning', 'grok-4.20-reasoning'],
    // No X.0 = X.
    ['mistral-ocr-4-0', 'mistral-ocr-4.0'],
    ['llama3-1-70b', 'llama3.1-70b'],
    ['meta.llama3-1-70b', 'meta.llama3.1-70b'],
  ])('writes the version of %s with a dot: %s', (name, folded) => {
    expect(foldVersions(name)).toBe(folded)
  })

  it.each([
    // sizes and context lengths
    'glm-4-9b',
    'llama-3-8b',
    'step-1-8k',
    'moonshot-v1-8k',
    'gpt-4-32k',
    // dates and zero-padded numbers
    'tts-1-1106',
    'gpt-4-1106-preview',
    'kimi-k2-0905-preview',
    'gpt-5.1-2025-11-13',
    'o1-2024-12-17',
    // a version followed by a date folds only once the date is peeled
    'claude-opus-4-5-20251101',
    // glued digits, underscores, Fireworks p, no separator
    'claude-opus-41',
    'stablelm-2-1_6b',
    'llama-v3p1',
    'gpt-35-turbo',
    // three-part versions
    'dolphin-2-9-2-qwen2-72b',
    'labs-leanstral-1-5-1',
  ])('leaves %s as written', (name) => {
    expect(foldVersions(name)).toBe(name)
  })

  it('drops the next-token condition for typed queries only', () => {
    expect(foldVersions('claude-opus-4-5-2025', true)).toBe(
      'claude-opus-4.5-2025'
    )
    expect(foldVersions('claude-opus-4-5-2025')).toBe('claude-opus-4-5-2025')
    expect(foldVersions('2025-11-13', true)).toBe('2025-11-13')
  })
})

describe('orgPrefixLength', () => {
  it.each([
    ['deepseek-ai/DeepSeek-V3', 12],
    ['hf:deepseek-ai/DeepSeek-R1-0528', 15],
    // two slashes, Cloudflare paths, a slash first
    ['Pro/deepseek-ai/DeepSeek-R1', 0],
    ['@cf/meta/llama-3-8b', 0],
    ['/x-1', 0],
    // the rest does not look like a model id
    ['agentcore/search', 0],
    ['openrouter/auto', 0],
  ])('%s drops %i characters', (name, length) => {
    expect(orgPrefixLength(name)).toBe(length)
  })
})

describe('spellingKey', () => {
  it('lower-cases, drops one org/ prefix and folds the last path segment only', () => {
    expect(spellingKey('deepseek-ai/DeepSeek-V3-2', true)).toBe('deepseek-v3.2')
    expect(spellingKey('Pro/deepseek-ai/DeepSeek-V3-2', true)).toBe(
      'pro/deepseek-ai/deepseek-v3.2'
    )
    expect(spellingKey('a-1-2/b-1-2/c-3-4', true)).toBe('a-1-2/b-1-2/c-3.4')
  })

  it('returns the text as written while the fold is off', () => {
    expect(spellingKey('deepseek-ai/DeepSeek-V3-2', false)).toBe(
      'deepseek-ai/DeepSeek-V3-2'
    )
  })
})

describe('isVersionDash', () => {
  /** Whether the dash right after `after` in `text` is a version dash. */
  function dashAfter(text: string, after: string): boolean {
    const index = text.indexOf(after) + after.length
    if (text[index] !== '-') throw new Error(`No dash after ${after}`)
    return isVersionDash(text, index)
  }

  it('finds the dash inside a version pair, whatever follows the pair', () => {
    expect(dashAfter('claude-opus-4-5-20251101', 'claude-opus-4')).toBe(true)
    expect(dashAfter('claude-opus-4-5-20251101', 'claude-opus-4-5')).toBe(false)
    expect(dashAfter('claude-opus-4-5-20251101', 'claude-opus')).toBe(false)
    expect(dashAfter('claude-opus-4-5@20251101', 'claude-opus-4')).toBe(true)
    expect(dashAfter('x-1-2-3', 'x-1')).toBe(true)
    expect(dashAfter('x-1-2-3', 'x-1-2')).toBe(false)
    expect(dashAfter('stablelm-2-1_6b', 'stablelm-2')).toBe(false)
  })

  it('never counts a dash before the last slash', () => {
    expect(dashAfter('org/a-1-2', 'org/a')).toBe(false)
    expect(isVersionDash('pro/deep-seek/x-1-2', 'pro/deep'.length)).toBe(false)
  })
})

describe('dateSuffix', () => {
  const format = (name: string) => dateSuffix(name)?.format ?? null

  it.each<[string, SnapshotFormat | null]>([
    // year range 2020-2039; a failed range falls through to the next format
    ['x-2019-07-18', 'MM-DD'],
    ['x-2020-07-18', 'YYYY-MM-DD'],
    ['x-2039-07-18', 'YYYY-MM-DD'],
    ['x-2040-07-18', 'MM-DD'],
    ['x-2025-07', 'YYYY-MM'],
    ['x-08-2024', 'MM-YYYY'],
    ['x-20251101', 'YYYYMMDD'],
    ['x-202605', 'YYYYMM'],
    // YY 23-39
    ['x-220615', null],
    ['x-230615', 'YYMMDD'],
    ['x-390615', 'YYMMDD'],
    ['x-400615', null],
    ['x-0731', 'MMDD'],
    ['x-2411', 'YYMM'],
    ['x-2400', null],
    ['x-2413', null],
    ['x-0001', null],
    ['x-0132', null],
    // MM-DD needs a zero-padded part or a month of 10 or more
    ['x-05-20', 'MM-DD'],
    ['x-10-25', 'MM-DD'],
    ['x-5-20', null],
    // only a colon in the last path segment blocks a date
    ['ft:gpt-4o-mini-2024-07-18:acme::id', null],
    ['ollama/qwen3:30b-a3b-2507', null],
    ['hf:deepseek-ai/DeepSeek-R1-0528', 'MMDD'],
    // thinking budgets are not dates; longer vendor snapshots are
    ['x-thinking-1024', null],
    ['x-thinking-2507', null],
    ['x-thinking-251104', 'YYMMDD'],
    ['x-thinking-20251109', 'YYYYMMDD'],
    // a MoE active size before a 4-digit or MM-DD date: a new weight set
    ['qwen3-235b-a22b-2507', null],
    ['qwen3-235b-a22b-07-25', null],
    ['qwen3-30b-a3b-0528', null],
    ['qwen3-235b-a22b-instruct-2507', 'YYMM'],
    ['x-a22b-250715', 'YYMMDD'],
    // Gemini: MMDD only after -exp / -preview
    ['gemini-2.5-flash-1024', null],
    ['gemini-exp-1206', 'MMDD'],
    // -00N only for Gemini, Imagen and Veo x.y names
    ['gemini-2.0-flash-001', '-00N'],
    ['text-embedding-ada-002', null],
    ['gemini-embedding-001', null],
    ['davinci-002', null],
    // Vertex @ forms
    ['x@20251101', '@YYYYMMDD'],
    ['x@2411-001', '@YYMM-00N'],
    ['x@2405', '@YYMM'],
    ['x@001', '@NNN'],
    ['x@latest', 'latest'],
    ['x@default', null],
    ['x-latest', 'latest'],
    ['x-LATEST', 'latest'],
    // the base must keep a letter and not end in - or @
    ['2024-07-18', null],
    ['1-0731', null],
    ['x--0731', null],
  ])('reads the end of %s as %s', (name, expected) => {
    expect(format(name)).toBe(expected)
  })

  it('labels the date as written, without the separator, and keeps the base', () => {
    expect(dateSuffix('gpt-4o-mini-2024-07-18')).toEqual({
      format: 'YYYY-MM-DD',
      label: '2024-07-18',
      base: 'gpt-4o-mini',
    })
    expect(dateSuffix('mistral-large@2411-001')).toEqual({
      format: '@YYMM-00N',
      label: '2411-001',
      base: 'mistral-large',
    })
  })

  it('reads only the end of the name', () => {
    expect(dateSuffix('o3-mini-2025-01-31-high')).toBeNull()
    expect(dateSuffix('gpt-4-1106-preview')).toBeNull()
  })
})

describe('snapshotSortKey', () => {
  it.each<[SnapshotFormat, string, number[]]>([
    ['YYYY-MM-DD', '2024-07-18', [2, 2024, 7, 18]],
    ['YYYYMMDD', '20251101', [2, 2025, 11, 1]],
    ['@YYYYMMDD', '20241022', [2, 2024, 10, 22]],
    ['YYMMDD', '250615', [2, 2025, 6, 15]],
    ['YYYYMM', '202605', [2, 2026, 5, 0]],
    ['YYYY-MM', '2025-07', [2, 2025, 7, 0]],
    ['MM-YYYY', '08-2024', [2, 2024, 8, 0]],
    ['YYMM', '2411', [2, 2024, 11, 0]],
    ['@YYMM', '2405', [2, 2024, 5, 0]],
    ['@YYMM-00N', '2411-001', [2, 2024, 11, 1]],
    ['-00N', '002', [1, 2, 0, 0]],
    ['@NNN', '001', [1, 1, 0, 0]],
    ['MMDD', '0731', [0, 7, 31, 0]],
    ['MM-DD', '05-20', [0, 5, 20, 0]],
    ['latest', 'latest', [0, 0, 0, 0]],
  ])('%s %s sorts as %j', (format, label, key) => {
    expect(snapshotSortKey(format, label)).toEqual(key)
  })
})

describe('queryForms', () => {
  it('folds a typed query strictly and relaxed, keeping a [tag] in lower case', () => {
    expect(queryForms('claude-opus-4-5-2025')).toEqual([
      'claude-opus-4-5-2025',
      'claude-opus-4.5-2025',
    ])
    expect(queryForms('  [正价KIRO]Claude-Opus-4-6 ')).toEqual([
      '[正价kiro]claude-opus-4.6',
    ])
    expect(queryForms('deepseek-ai/DeepSeek-V3')).toEqual(['deepseek-v3'])
  })

  it('also reads a dash typed right after a version major as the dot of the other spelling', () => {
    expect(queryForms('claude-opus-4-')).toEqual([
      'claude-opus-4-',
      'claude-opus-4.',
    ])
    expect(queryForms('[正价KIRO]claude-opus-4-')).toEqual([
      '[正价kiro]claude-opus-4-',
      '[正价kiro]claude-opus-4.',
    ])
    expect(queryForms('deepseek-ai/DeepSeek-V3-')).toEqual([
      'deepseek-v3-',
      'deepseek-v3.',
    ])
  })

  it.each([
    // the version is complete, a date, or no version at all
    ['claude-opus-4.5-', ['claude-opus-4.5-']],
    ['claude-opus-4-5-', ['claude-opus-4.5-']],
    ['2025-11-', ['2025-11-']],
    ['claude-', ['claude-']],
    ['gpt-4o-', ['gpt-4o-']],
  ])('adds no dotted form to %s', (query, forms) => {
    expect(queryForms(query)).toEqual(forms)
  })

  it('has no form for a blank query', () => {
    expect(queryForms('   ')).toEqual([])
  })
})

describe('splitLineTag', () => {
  it('splits the leading [...] tag off', () => {
    expect(splitLineTag('[按次]deepseek-v4-flash')).toEqual({
      line: '按次',
      rest: 'deepseek-v4-flash',
    })
    expect(splitLineTag('deepseek-v4-flash')).toEqual({
      line: '',
      rest: 'deepseek-v4-flash',
    })
  })
})
