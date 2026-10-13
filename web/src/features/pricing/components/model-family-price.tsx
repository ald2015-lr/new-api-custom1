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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { getCurrencyLabel } from '@/lib/currency'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { DEFAULT_TOKEN_UNIT } from '../constants'
import { useBillingTime, usesBillingTime } from '../hooks/use-billing-time'
import {
  getDynamicDisplayGroupRatio,
  getDynamicPriceUnitLabelKey,
  getDynamicPricingSummary,
  isUnconfiguredTaskUsageModel,
  type DynamicPriceEntry,
} from '../lib/dynamic-price'
import {
  getLowestPricePoint,
  type MissingPriceReason,
  type ModelPricePoint,
} from '../lib/model-family'
import { isTokenBasedModel } from '../lib/model-helpers'
import type { ModelFamily } from '../lib/model-variants'
import { formatPrice, formatRequestPrice } from '../lib/price'
import { taskPriceLabel, taskUsageUnitLabel } from '../lib/task-price-display'
import type { PricingModel } from '../types'
import type { ModelPriceCellOptions } from './model-price-cell'

function useResolvedPriceOptions(options: ModelPriceCellOptions | undefined) {
  // Price formatters read the display currency from this store.
  const currency = useSystemConfigStore((state) => state.config.currency)
  return useMemo(
    () => ({
      tokenUnit: options?.tokenUnit ?? DEFAULT_TOKEN_UNIT,
      priceRate: options?.priceRate ?? 1,
      usdExchangeRate: options?.usdExchangeRate ?? 1,
      showRechargePrice: options?.showRechargePrice ?? false,
      selectedGroup: options?.selectedGroup,
      currency,
    }),
    [
      options?.tokenUnit,
      options?.priceRate,
      options?.usdExchangeRate,
      options?.showRechargePrice,
      options?.selectedGroup,
      currency,
    ]
  )
}

function useDynamicEntryUnitLabel() {
  const { t, i18n } = useTranslation()
  return (entry: DynamicPriceEntry, tokenUnitLabel: string) => {
    const unitLabelKey = getDynamicPriceUnitLabelKey(entry)
    return taskUsageUnitLabel(
      entry,
      i18n.language,
      unitLabelKey ? t(unitLabelKey) : tokenUnitLabel
    )
  }
}

function toDate(time: number | undefined): Date | undefined {
  return time === undefined ? undefined : new Date(time)
}

/** Same wording as the variant rows and the table's price cells. */
function missingPriceText(t: TFunction, reason: MissingPriceReason): string {
  switch (reason) {
    case 'unconfigured':
      return t('Not configured')
    case 'special':
      return t('Special billing expression')
    default:
      return t('Dynamic Pricing')
  }
}

/**
 * Lowest price of each line of a family. `chips` is the compact card layout
 * (`按次  from $0.9 / request`); `list` is the table cell, formatted like the
 * variant rows below it: a bare value plus a currency and unit caption.
 */
export function FamilyLinePrices(props: {
  family: ModelFamily
  options?: ModelPriceCellOptions
  layout: 'chips' | 'list'
  className?: string
}) {
  const { t } = useTranslation()
  const options = useResolvedPriceOptions(props.options)
  const entryUnitLabel = useDynamicEntryUnitLabel()
  const tokenUnitLabel = options.tokenUnit === 'K' ? '1K' : '1M'
  const isList = props.layout === 'list'
  // One clock for the family: every time-of-day price of its variants follows
  // the same minute, like the model cards and table cells do.
  const timeExpression = useMemo(
    () =>
      props.family.models.find((model) => usesBillingTime(model.billing_expr))
        ?.billing_expr,
    [props.family]
  )
  const billingTime = useBillingTime(timeExpression)
  const lines = useMemo(() => {
    const pointOptions = {
      ...options,
      now: toDate(billingTime),
      showCurrencySymbol: !isList,
    }
    return props.family.lines.map((line) => ({
      line: line.line,
      price: getLowestPricePoint(line.variants, pointOptions),
    }))
  }, [props.family, options, billingTime, isList])
  const showLineNames =
    lines.length > 1 || lines.some((line) => line.line !== '')

  const unitOf = (point: ModelPricePoint) => {
    if (point.kind === 'request') return t('request')
    if (point.kind === 'token') return tokenUnitLabel
    return entryUnitLabel(point.entry, tokenUnitLabel)
  }

  if (isList) {
    const currencyLabel =
      options.currency.quotaDisplayType === 'TOKENS'
        ? 'USD'
        : getCurrencyLabel()
    const captionOf = (point: ModelPricePoint) =>
      point.kind === 'token'
        ? t('{{currency}} / {{unit}} tokens', {
            currency: currencyLabel,
            unit: tokenUnitLabel,
          })
        : `${currencyLabel} / ${unitOf(point)}`
    // One wrapping row per line: chip and value stay together, the caption
    // drops below them when the column is narrow.
    return (
      <ul className={cn('flex min-w-0 flex-col gap-1.5', props.className)}>
        {lines.map(({ line, price }) => {
          const lineName = line || t('Default')
          return (
            <li
              key={line}
              className='flex min-w-0 flex-wrap items-baseline gap-x-1.5 gap-y-0.5 whitespace-normal'
            >
              {showLineNames && (
                <Badge
                  variant='outline'
                  className='max-w-[7rem] font-normal'
                  title={lineName}
                >
                  <span className='truncate'>{lineName}</span>
                </Badge>
              )}
              {price.point ? (
                <>
                  <span className='font-mono text-sm whitespace-nowrap tabular-nums'>
                    {t('from {{price}}', { price: price.point.formatted })}
                  </span>
                  <span
                    data-table-text='secondary'
                    className='text-muted-foreground text-xs break-words'
                  >
                    {captionOf(price.point)}
                  </span>
                </>
              ) : (
                <span className='text-muted-foreground text-sm'>
                  {missingPriceText(t, price.reason)}
                </span>
              )}
            </li>
          )
        })}
      </ul>
    )
  }

  return (
    <ul className={cn('flex min-w-0 flex-wrap gap-1.5', props.className)}>
      {lines.map(({ line, price }) => {
        const lineName = line || t('Default')
        const priceText = price.point
          ? t('from {{price}}', {
              price: `${price.point.formatted} / ${unitOf(price.point)}`,
            })
          : missingPriceText(t, price.reason)
        return (
          <li key={line} className='max-w-full min-w-0'>
            <Badge
              variant='outline'
              className='h-auto min-h-5 max-w-full justify-start gap-1.5 font-normal'
              title={showLineNames ? `${lineName} · ${priceText}` : priceText}
            >
              {/* Only the line name gives way: the price is what the chip is for. */}
              {showLineNames && (
                <span className='text-foreground max-w-[10rem] min-w-0 truncate font-medium'>
                  {lineName}
                </span>
              )}
              <span className='text-muted-foreground shrink-0 font-mono tabular-nums'>
                {priceText}
              </span>
            </Badge>
          </li>
        )
      })}
    </ul>
  )
}

type PricePart = { label?: string; value: string; unit: string }

/**
 * One-line price of a single variant: `$0.9 / request`,
 * `Input $x  Output $y  Cached $z / 1M`, or the leading entries of an
 * expression price. Time-of-day prices follow the shared one-minute clock.
 */
export function VariantPrice(props: {
  model: PricingModel
  options?: ModelPriceCellOptions
  className?: string
}) {
  const { t, i18n } = useTranslation()
  const options = useResolvedPriceOptions(props.options)
  const entryUnitLabel = useDynamicEntryUnitLabel()
  const tokenUnitLabel = options.tokenUnit === 'K' ? '1K' : '1M'
  const billingTime = useBillingTime(props.model.billing_expr)
  const summary = useMemo(
    () =>
      getDynamicPricingSummary(props.model, {
        ...options,
        now: toDate(billingTime),
        groupRatioMultiplier: getDynamicDisplayGroupRatio(
          props.model,
          options.selectedGroup
        ),
      }),
    [props.model, options, billingTime]
  )

  const tokenPart = (type: 'input' | 'output' | 'cache', label: string) => ({
    label,
    value: formatPrice(
      props.model,
      type,
      options.tokenUnit,
      options.showRechargePrice,
      options.priceRate,
      options.usdExchangeRate,
      options.selectedGroup
    ),
    unit: tokenUnitLabel,
  })

  let parts: PricePart[] = []
  let note: string | null = null
  if (summary) {
    if (summary.isSpecialExpression) {
      note = t('Special billing expression')
    } else {
      const entries = summary.primaryEntries.slice(0, 2)
      parts = entries.map((entry) => {
        let label: string | undefined
        if (entries.length > 1) {
          label =
            entry.labelKind === 'schema'
              ? taskPriceLabel(
                  entry.description,
                  entry.shortLabel,
                  i18n.language
                )
              : t(entry.shortLabel)
        }
        return {
          label,
          value: entry.formattedRange ?? entry.formatted,
          unit: entryUnitLabel(entry, tokenUnitLabel),
        }
      })
      if (parts.length === 0) {
        note = summary.hasUnconfiguredProviders
          ? t('Not configured')
          : t('Dynamic Pricing')
      }
    }
  } else if (isUnconfiguredTaskUsageModel(props.model)) {
    note = t('Not configured')
  } else if (isTokenBasedModel(props.model)) {
    parts = [tokenPart('input', t('Input')), tokenPart('output', t('Output'))]
    if (props.model.cache_ratio != null) {
      parts.push(tokenPart('cache', t('Cached')))
    }
  } else {
    parts = [
      {
        value: formatRequestPrice(
          props.model,
          options.showRechargePrice,
          options.priceRate,
          options.usdExchangeRate,
          options.selectedGroup
        ),
        unit: t('request'),
      },
    ]
  }

  if (note) {
    return (
      <span className={cn('text-muted-foreground text-xs', props.className)}>
        {note}
      </span>
    )
  }

  // Labels separate the parts, so a wrapped line never starts with a dot.
  const sharedUnit = parts.every((part) => part.unit === parts[0]?.unit)
  return (
    <span
      className={cn(
        'flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-0.5',
        props.className
      )}
    >
      {parts.map((part, index) => (
        <span
          key={part.label ?? index}
          className='inline-flex items-baseline gap-1 whitespace-nowrap'
        >
          {part.label && (
            <span className='text-muted-foreground text-xs'>{part.label}</span>
          )}
          <span className='font-mono text-sm font-semibold tabular-nums'>
            {part.value}
          </span>
          {(!sharedUnit || index === parts.length - 1) && (
            <span className='text-muted-foreground text-xs'>/ {part.unit}</span>
          )}
        </span>
      ))}
      {summary?.isTimePricing && (
        <span className='text-muted-foreground basis-full text-xs'>
          {t('Current period price')}
        </span>
      )}
    </span>
  )
}
