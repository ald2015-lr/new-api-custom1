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
import {
  buildModelSquareIndex,
  groupModelFamilies,
  resolveModelSquareSettings,
  stripLineTag,
  type ModelSquareSettings,
} from '../lib/model-variants'
import type { PricingModel } from '../types'

/** Both folds off: the grouping without folds (names as written). */
export const LEGACY: Partial<ModelSquareSettings> = {
  version_spelling_fold_enabled: false,
  snapshot_fold_enabled: false,
}

/** The settings an older backend implies, with overrides. */
export function squareSettings(
  overrides: Partial<ModelSquareSettings> = {}
): ModelSquareSettings {
  return { ...resolveModelSquareSettings(undefined), ...overrides }
}

/** Index the list as the whole catalogue and group it, like the page. */
export function familiesOf(
  models: PricingModel[],
  overrides: Partial<ModelSquareSettings> = {}
) {
  return groupModelFamilies(
    models,
    buildModelSquareIndex(models, squareSettings(overrides))
  )
}

/**
 * The vendor the backend's built-in rules give a fixture name (only the
 * vendors the fixtures need). The card title prefers dashed versions for
 * Anthropic, ByteDance, NovelAI, Kling and Mistral names.
 */
export function fixtureVendor(name: string): string | undefined {
  const n = stripLineTag(name).toLowerCase().replace(/^ft:/, '')
  if (n.includes('claude')) return 'Anthropic'
  if (n.includes('deepseek')) return 'DeepSeek'
  if (
    /^(?:gpt-|tts-|text-embedding-(?:ada|3)|chatgpt-|omni-moderation|o\d(?:-|$)|davinci|babbage|text-davinci)/.test(
      n
    )
  ) {
    return 'OpenAI'
  }
  if (/doubao|^seed-/.test(n)) return '字节跳动'
  if (/^(?:gemini|imagen|veo|text-embedding-00|text-bison|lyria)/.test(n)) {
    return 'Google'
  }
  if (n.startsWith('glm')) return '智谱'
  if (/kimi|moonshot/.test(n)) return 'Moonshot'
  if (/qwen|^text-embedding-v/.test(n)) return '阿里巴巴'
  if (/mistral|codestral|mixtral|leanstral/.test(n)) return 'Mistral'
  if (n.startsWith('llama')) return 'Meta'
  if (n.startsWith('ernie')) return '百度'
  if (n.startsWith('hunyuan')) return '腾讯'
  if (/^(?:command|c4ai)/.test(n)) return 'Cohere'
  if (n.startsWith('grok')) return 'xAI'
  if (n.startsWith('speech-')) return 'MiniMax'
  if (n.startsWith('nai-diffusion')) return 'NovelAI'
  return undefined
}

let nextId = 1

/** A per-request model with the fixture vendor of its name. */
export function squareModel(
  name: string,
  overrides: Partial<PricingModel> = {}
): PricingModel {
  return {
    id: nextId++,
    model_name: name,
    vendor_name: fixtureVendor(name),
    quota_type: 1,
    model_ratio: 1,
    completion_ratio: 1,
    model_price: 1,
    enable_groups: ['default'],
    group_ratio: { default: 1 },
    ...overrides,
  }
}

/** Models for a list of names, see squareModel. */
export function squareModels(names: readonly string[]): PricingModel[] {
  return names.map((name) => squareModel(name))
}

/**
 * The owner's screenshots as one catalogue (dashed spelling on the untagged,
 * 按量 and 官KEY lines, dotted only on 正价kiro, plus the dated snapshots and
 * the OpenAI speech and embedding names), with the versions written as 4.x.
 */
export const SCREENSHOT_CATALOGUE = [
  'claude-opus-4-6',
  'claude-opus-4-6-thinking',
  '[按量]claude-opus-4-6',
  '[官KEY]claude-opus-4-6',
  '[正价kiro]claude-opus-4.6',
  '[正价kiro]claude-opus-4.6-thinking',
  '[正价kiro]claude-opus-4.6-thinking-xhigh',
  '[正价kiro]claude-opus-4.6-thinking-max',
  'claude-sonnet-4-6',
  '[正价kiro]claude-sonnet-4.6',
  'gpt-4o-mini',
  'gpt-4o-mini-2024-07-18',
  'tts-1',
  'tts-1-1106',
  'tts-1-hd',
  'tts-1-hd-1106',
  'text-embedding-ada-002',
  'text-embedding-3-small',
  '[按次]deepseek-v4-flash',
  '[按次]deepseek-v4-flash-0731',
  'deepseek-v4-pro',
  'deepseek-v4-pro-0813',
  '[正价kiro]claude-opus-4-5',
  'claude-opus-4-5-20251101',
  'claude-haiku-4-5',
  'claude-haiku-4-5-20251001',
  '[按次]claude-haiku-4-6',
] as const
