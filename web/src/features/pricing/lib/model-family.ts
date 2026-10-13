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
import type { TFunction } from 'i18next'

import type { SuccessRatePoint } from '@/features/performance-metrics/types'

import {
  EXCLUDED_GROUPS,
  FILTER_ALL,
  QUOTA_TYPE_VALUES,
  SORT_OPTIONS,
} from '../constants'
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
  type ModelFamilyLine,
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

function isValidRate(rate: number | undefined): rate is number {
  return rate != null && Number.isFinite(rate) && rate >= 0 && rate <= 100
}

function latencyOf(sample: ModelPerfSample): number {
  return Number.isFinite(sample.avg_latency_ms) && sample.avg_latency_ms > 0
    ? sample.avg_latency_ms
    : Number.POSITIVE_INFINITY
}

/** Negative when `a` is the healthier variant. */
function compareHealth(a: ModelPerfSample, b: ModelPerfSample): number {
  const rateA = isValidRate(a.success_rate) ? a.success_rate : -1
  const rateB = isValidRate(b.success_rate) ? b.success_rate : -1
  if (rateA !== rateB) return rateB - rateA
  const requestsA = a.request_count ?? 0
  const requestsB = b.request_count ?? 0
  if (requestsA !== requestsB) return requestsB - requestsA
  return latencyOf(a) - latencyOf(b)
}

/**
 * The performance a family card shows: its healthiest variant, i.e. the
 * highest success rate (ties: more requests, then lower latency). The status
 * bar, latency and throughput all come from that one variant so the line
 * describes a real model rather than a blend; the expanded list still shows
 * every variant on its own.
 */
export function pickBestModelPerf(
  samples: readonly (ModelPerfSample | undefined)[]
): ModelPerfSample | undefined {
  let best: ModelPerfSample | undefined
  for (const sample of samples) {
    if (sample == null) continue
    if (best == null || compareHealth(sample, best) < 0) best = sample
  }
  return best
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

function pricePointKey(result: PricePointResult): string {
  return result.point
    ? `${pricePointUnitKey(result.point)}:${result.point.value}`
    : `missing:${result.reason}`
}

function groupRatioOf(
  ratios: Record<string, number> | undefined,
  group: string
): number | undefined {
  const ratio = ratios?.[group]
  return typeof ratio === 'number' && Number.isFinite(ratio) ? ratio : undefined
}

function sortGroupsByRatio(
  groups: Iterable<string>,
  ratios: Record<string, number> | undefined
): string[] {
  return unique(groups).sort(
    (a, b) =>
      (groupRatioOf(ratios, a) ?? 1) - (groupRatioOf(ratios, b) ?? 1) ||
      a.localeCompare(b)
  )
}

/**
 * Groups a model is priced in for the viewer, cheapest first: the selected
 * group while a group filter is active, otherwise every enabled group that
 * has a ratio (the groups the summary price takes its lowest ratio from), or
 * every enabled group when none has one.
 */
export function getModelPriceGroups(
  model: PricingModel,
  selectedGroup?: string
): string[] {
  const enabled = Array.isArray(model.enable_groups) ? model.enable_groups : []
  if (
    selectedGroup &&
    selectedGroup !== FILTER_ALL &&
    enabled.includes(selectedGroup)
  ) {
    return [selectedGroup]
  }
  const candidates = enabled.filter((group) => !EXCLUDED_GROUPS.includes(group))
  const priced = candidates.filter(
    (group) => groupRatioOf(model.group_ratio, group) !== undefined
  )
  // Without any configured ratio every group bills at 1, like the summary.
  return sortGroupsByRatio(
    priced.length > 0 ? priced : candidates,
    model.group_ratio
  )
}

/**
 * What a model's price shows in one group, or null when no shown price
 * depends on the group (special expression, not configured, no entries).
 */
function groupPriceSignature(
  model: PricingModel,
  group: string
): string | null {
  const summary = getDynamicPricingSummary(model, {
    tokenUnit: 'M',
    groupRatioMultiplier: getDynamicDisplayGroupRatio(model, group),
  })
  if (summary) {
    if (summary.isSpecialExpression || summary.primaryEntries.length === 0) {
      return null
    }
    return summary.primaryEntries
      .map(
        (entry) => `${entry.field}:${entry.formattedRange ?? entry.formatted}`
      )
      .join('|')
  }
  if (isUnconfiguredTaskUsageModel(model)) return null
  const ratio = getDisplayGroupRatio(model, group)
  if (isTokenBasedModel(model)) {
    return Number.isFinite(model.model_ratio)
      ? `token:${model.model_ratio * ratio}`
      : null
  }
  if (model.quota_type !== QUOTA_TYPE_VALUES.REQUEST) return null
  const price = model.model_price ?? 0
  return Number.isFinite(price) ? `request:${price * ratio}` : null
}

/**
 * The groups to list a model's price under one by one: its price groups when
 * the prices shown for them differ, otherwise none (one price covers them
 * all, e.g. a free model or a special expression).
 */
export function getSplitPriceGroups(
  model: PricingModel,
  selectedGroup?: string
): string[] {
  const groups = getModelPriceGroups(model, selectedGroup)
  if (groups.length < 2) return []
  const signatures = groups.map((group) => groupPriceSignature(model, group))
  if (signatures.some((signature) => signature === null)) return []
  return new Set(signatures).size > 1 ? groups : []
}

/** Whether the table's Cached column has a price to show for the model. */
export function hasCachedPrice(model: PricingModel): boolean {
  const summary = getDynamicPricingSummary(model, { tokenUnit: 'M' })
  if (summary) {
    return (
      !summary.isSpecialExpression &&
      summary.entries.some(
        (entry) =>
          entry.field === 'cacheReadPrice' || entry.field === 'imageCachePrice'
      )
    )
  }
  return (
    !isUnconfiguredTaskUsageModel(model) &&
    isTokenBasedModel(model) &&
    model.cache_ratio != null
  )
}

/** Every group any variant of the line is priced in, cheapest first. */
export function getLineGroups(
  line: ModelFamilyLine,
  selectedGroup?: string
): string[] {
  return sortGroupsByRatio(
    line.variants.flatMap((variant) =>
      getModelPriceGroups(variant.model, selectedGroup)
    ),
    line.variants[0]?.model.group_ratio
  )
}

/** Tags of a family's tagged lines. */
export function getFamilyTags(family: ModelFamily): Set<string> {
  return new Set(family.lines.map((line) => line.line).filter(Boolean))
}

/**
 * Name of a line: its tag, or for untagged models the groups they are sold
 * in, so a line reads `Anthropic` rather than a bare `Default`. When one of
 * those groups is also the tag of another line in the family (`x` in group
 * 按量 next to `[按量]x`), the untagged line keeps `Default` in front so the
 * two never share a name.
 */
export function getLineDisplayName(
  t: TFunction,
  line: string,
  groups: readonly string[],
  familyTags: ReadonlySet<string>
): string {
  if (line) return line
  const name = groups.join(' / ')
  if (!name) return t('Default')
  if (groups.some((group) => familyTags.has(group))) {
    return `${t('Default')} · ${name}`
  }
  return name
}

export type LinePriceEntry = {
  key: string
  /** The line tag; '' for untagged models. */
  line: string
  /** Set when the entry prices one group of a line split by group. */
  group?: string
  /** Groups the entry covers, cheapest first. */
  groups: string[]
  price: PricePointResult
}

/**
 * Lowest price of each line. A line whose groups price it differently is
 * split into one entry per group (the lowest price within that group), so a
 * card never presents another group's cheaper price as this one's.
 */
export function getFamilyLinePrices(
  family: ModelFamily,
  options: PricePointOptions
): LinePriceEntry[] {
  return family.lines.flatMap((line) => {
    const groups = getLineGroups(line, options.selectedGroup)
    const whole: LinePriceEntry = {
      key: line.line,
      line: line.line,
      groups,
      price: getLowestPricePoint(line.variants, options),
    }
    // A variant without a priced group would belong to no split entry.
    const unpriced = line.variants.some(
      (variant) =>
        getModelPriceGroups(variant.model, options.selectedGroup).length === 0
    )
    if (groups.length < 2 || unpriced) return [whole]
    const byGroup = groups.map((group): LinePriceEntry => {
      const members = line.variants.filter((variant) =>
        getModelPriceGroups(variant.model, options.selectedGroup).includes(
          group
        )
      )
      return {
        key: JSON.stringify([line.line, group]),
        line: line.line,
        group,
        groups: [group],
        price: getLowestPricePoint(members, {
          ...options,
          selectedGroup: group,
        }),
      }
    })
    const firstKey = pricePointKey(byGroup[0].price)
    return byGroup.every((entry) => pricePointKey(entry.price) === firstKey)
      ? [whole]
      : byGroup
  })
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
