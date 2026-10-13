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
import type { SuccessRatePoint } from '@/features/performance-metrics/types'

import { QUOTA_TYPE_VALUES, SORT_OPTIONS } from '../constants'
import type { PricingModel } from '../types'
import {
  getDynamicDisplayGroupRatio,
  getDynamicPricingSummary,
  isUnconfiguredTaskUsageModel,
  type DynamicPriceEntry,
  type DynamicPriceOptions,
} from './dynamic-price'
import { getModelPrice, parseTags } from './filters'
import { getDisplayGroupRatio, isTokenBasedModel } from './model-helpers'
import {
  stripLineTag,
  type ModelFamily,
  type ModelVariant,
} from './model-variants'
import { formatPrice, formatRequestPrice } from './price'

// ----------------------------------------------------------------------------
// Family aggregates: lowest price per line, combined performance, the union
// of groups and endpoints, and the order of families for the active sort
// ----------------------------------------------------------------------------

export type ModelPerfSample = {
  window_start?: number
  window_end?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_series?: SuccessRatePoint[]
  /** Requests in the window; the public summary may omit it. */
  request_count?: number
}

/** Below this success rate a slot is critical (rendered red). */
const CRITICAL_SUCCESS_RATE = 70

function isValidRate(rate: number | undefined): rate is number {
  return rate != null && Number.isFinite(rate) && rate >= 0 && rate <= 100
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b)
  const middle = Math.floor(sorted.length / 2)
  return sorted.length % 2 === 0
    ? (sorted[middle - 1] + sorted[middle]) / 2
    : sorted[middle]
}

function weightedMean(values: { value: number; weight: number }[]): number {
  const total = values.reduce((sum, item) => sum + item.weight, 0)
  return values.reduce((sum, item) => sum + item.value * item.weight, 0) / total
}

/**
 * Combine the performance of a family's variants into one badge value.
 *
 * - Success rate: weighted by request counts when every sampled variant
 *   reports one, otherwise the mean of the available rates.
 * - Latency and throughput: request-weighted mean with counts, otherwise the
 *   median of the variants that have a value (robust to one slow variant).
 * - Hourly slots: the same weighted/plain mean per slot, except that a slot
 *   takes the worst variant rate when any variant is critical in that hour, so
 *   a badly failing variant always shows up as a red slot.
 */
export function aggregateModelPerf(
  samples: readonly (ModelPerfSample | undefined)[]
): ModelPerfSample | undefined {
  const present = samples.filter(
    (sample): sample is ModelPerfSample => sample != null
  )
  if (present.length === 0) return undefined
  const weighted = present.every((sample) => (sample.request_count ?? 0) > 0)
  const weightOf = (sample: ModelPerfSample) =>
    weighted ? (sample.request_count ?? 1) : 1

  const rated = present.filter((sample) => isValidRate(sample.success_rate))
  const successRate =
    rated.length > 0
      ? weightedMean(
          rated.map((sample) => ({
            value: sample.success_rate,
            weight: weightOf(sample),
          }))
        )
      : Number.NaN

  const combine = (pick: (sample: ModelPerfSample) => number) => {
    const values = present.filter(
      (sample) => Number.isFinite(pick(sample)) && pick(sample) > 0
    )
    if (values.length === 0) return 0
    if (weighted) {
      return weightedMean(
        values.map((sample) => ({
          value: pick(sample),
          weight: weightOf(sample),
        }))
      )
    }
    return median(values.map(pick))
  }

  const slots = new Map<number, { value: number; weight: number }[]>()
  for (const sample of present) {
    for (const point of sample.recent_success_series ?? []) {
      if (!isValidRate(point.success_rate)) continue
      const slot = slots.get(point.ts) ?? []
      slot.push({ value: point.success_rate, weight: weightOf(sample) })
      slots.set(point.ts, slot)
    }
  }
  const series = [...slots.entries()]
    .sort(([a], [b]) => a - b)
    .map(([ts, values]) => {
      const worst = Math.min(...values.map((item) => item.value))
      return {
        ts,
        success_rate:
          worst < CRITICAL_SUCCESS_RATE ? worst : weightedMean(values),
      }
    })

  const windowed = present.find((sample) => sample.window_start != null)
  return {
    window_start: windowed?.window_start,
    window_end: windowed?.window_end,
    success_rate: successRate,
    avg_latency_ms: combine((sample) => sample.avg_latency_ms),
    avg_tps: combine((sample) => sample.avg_tps),
    recent_success_series: series,
  }
}

export type ModelPricePoint =
  | { kind: 'request'; value: number; formatted: string }
  | { kind: 'token'; value: number; formatted: string }
  | {
      kind: 'dynamic'
      value: number
      formatted: string
      entry: DynamicPriceEntry
    }

/**
 * Why a model or a line has no comparable headline price: the price is not
 * configured, it is a special (non-tiered) expression, or it is another
 * dynamic price without a primary entry.
 */
export type MissingPriceReason = 'unconfigured' | 'special' | 'dynamic'

export type PricePointResult =
  | { point: ModelPricePoint; reason?: undefined }
  | { point: null; reason: MissingPriceReason }

export type PricePointOptions = DynamicPriceOptions & { selectedGroup?: string }

function missing(reason: MissingPriceReason): PricePointResult {
  return { point: null, reason }
}

/**
 * The headline price of one model: the per-request price, the input price per
 * token unit, or the first primary entry of an expression price. `value` is in
 * USD after the display group ratio, comparable between points of one kind.
 * `options.now` pins the time of time-of-day prices.
 */
export function getModelPricePoint(
  model: PricingModel,
  options: PricePointOptions
): PricePointResult {
  const groupRatioMultiplier = getDynamicDisplayGroupRatio(
    model,
    options.selectedGroup
  )
  const summary = getDynamicPricingSummary(model, {
    ...options,
    groupRatioMultiplier,
  })
  if (summary) {
    if (summary.isSpecialExpression) return missing('special')
    const entry = summary.primaryEntries[0]
    if (!entry) {
      return missing(
        summary.hasUnconfiguredProviders ? 'unconfigured' : 'dynamic'
      )
    }
    return {
      point: {
        kind: 'dynamic',
        value: entry.value * groupRatioMultiplier,
        formatted: entry.formatted,
        entry,
      },
    }
  }
  if (isUnconfiguredTaskUsageModel(model)) return missing('unconfigured')

  const groupRatio = getDisplayGroupRatio(model, options.selectedGroup)
  if (isTokenBasedModel(model)) {
    if (!Number.isFinite(model.model_ratio)) return missing('unconfigured')
    return {
      point: {
        kind: 'token',
        value: model.model_ratio * 2 * groupRatio,
        formatted: formatPrice(
          model,
          'input',
          options.tokenUnit,
          options.showRechargePrice,
          options.priceRate,
          options.usdExchangeRate,
          options.selectedGroup,
          options.showCurrencySymbol
        ),
      },
    }
  }
  if (model.quota_type !== QUOTA_TYPE_VALUES.REQUEST) return missing('dynamic')
  if (!Number.isFinite(model.model_price ?? 0)) return missing('unconfigured')
  return {
    point: {
      kind: 'request',
      value: (model.model_price ?? 0) * groupRatio,
      formatted: formatRequestPrice(
        model,
        options.showRechargePrice,
        options.priceRate,
        options.usdExchangeRate,
        options.selectedGroup,
        options.showCurrencySymbol
      ),
    },
  }
}

function pricePointUnitKey(point: ModelPricePoint): string {
  return point.kind === 'dynamic'
    ? `dynamic:${point.entry.unit}:${point.entry.field}`
    : point.kind
}

/**
 * Lowest headline price of a line. Only prices of the same kind and unit are
 * compared: the kind of the line's first priced variant (display order) wins.
 * Without any priced variant the reason is shared by every variant, or
 * 'dynamic' when the variants disagree.
 */
export function getLowestPricePoint(
  variants: readonly ModelVariant[],
  options: PricePointOptions
): PricePointResult {
  let lowest: ModelPricePoint | null = null
  const reasons = new Set<MissingPriceReason>()
  for (const variant of variants) {
    const result = getModelPricePoint(variant.model, options)
    if (!result.point) {
      reasons.add(result.reason)
      continue
    }
    const point = result.point
    if (
      !lowest ||
      (pricePointUnitKey(point) === pricePointUnitKey(lowest) &&
        point.value < lowest.value)
    ) {
      lowest = point
    }
  }
  if (lowest) return { point: lowest }
  return missing(reasons.size === 1 ? [...reasons][0] : 'dynamic')
}

function unique(values: Iterable<string>): string[] {
  return [...new Set(values)]
}

export type FamilyUnion = {
  tags: string[]
  endpoints: string[]
  groups: string[]
}

/** Tags, endpoints and groups of every variant, in display order. */
export function getFamilyUnion(family: ModelFamily): FamilyUnion {
  const models = family.variants.map((variant) => variant.model)
  return {
    tags: unique(models.flatMap((model) => parseTags(model.tags))),
    endpoints: unique(
      models.flatMap((model) => model.supported_endpoint_types ?? [])
    ),
    groups: unique(models.flatMap((model) => model.enable_groups ?? [])),
  }
}

/** Name a family is listed under: its base, or a single model's untagged name. */
function familySortName(family: ModelFamily): string {
  return family.models.length === 1
    ? stripLineTag(family.models[0].model_name || '')
    : family.base
}

function lowestSortPrice(family: ModelFamily): number {
  return Math.min(...family.models.map(getModelPrice))
}

/**
 * Order families for the active sort. Grouping keeps the order in which each
 * family's first member appears, which is right for Price: Low to High (the
 * cheapest member leads). Name sorts by the title a family shows, without the
 * line tag, so the A-Z order does not restart once per line. Price: High to
 * Low ranks by the lowest member price, the one the family's chips show.
 */
export function sortModelFamilies(
  families: ModelFamily[],
  sortBy: string
): ModelFamily[] {
  if (sortBy === SORT_OPTIONS.NAME) {
    return [...families].sort(
      (a, b) =>
        familySortName(a).localeCompare(familySortName(b)) ||
        (a.models[0].model_name || '').localeCompare(
          b.models[0].model_name || ''
        )
    )
  }
  if (sortBy === SORT_OPTIONS.PRICE_HIGH) {
    return [...families].sort((a, b) => lowestSortPrice(b) - lowestSortPrice(a))
  }
  return families
}
