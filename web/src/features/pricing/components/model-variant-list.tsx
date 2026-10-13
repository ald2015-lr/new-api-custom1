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
import { ChevronRight } from 'lucide-react'
import { memo, useId } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import type {
  ModelFamily,
  ModelFamilyLine,
  ModelVariant,
} from '../lib/model-variants'
import { VariantPrice } from './model-family-price'
import { ModelPerfBadge, type ModelPerfBadgeData } from './model-perf-badge'
import type { ModelPriceCellOptions } from './model-price-cell'

export interface ModelVariantListProps {
  family: ModelFamily
  perfMap: ReadonlyMap<string, ModelPerfBadgeData>
  priceOptions: ModelPriceCellOptions
  onModelClick: (modelName: string) => void
  className?: string
}

/**
 * Every variant of a family grouped by line, each with its price, health,
 * copy (full model name) and details actions. Rows switch from a stacked
 * mobile layout to aligned columns once the list is wide enough.
 */
export const ModelVariantList = memo(function ModelVariantList(
  props: ModelVariantListProps
) {
  const showLineHeaders =
    props.family.lines.length > 1 ||
    props.family.lines.some((line) => line.line !== '')

  return (
    <div
      className={cn(
        '@container/variants divide-border/70 min-w-0 divide-y overflow-hidden rounded-lg border',
        // Long lists scroll inside the panel instead of pushing the page.
        'max-h-[min(70vh,36rem)] overflow-y-auto overscroll-contain',
        props.className
      )}
    >
      {props.family.lines.map((line) => (
        <VariantLine
          key={line.line}
          line={line}
          showHeader={showLineHeaders}
          perfMap={props.perfMap}
          priceOptions={props.priceOptions}
          onModelClick={props.onModelClick}
        />
      ))}
    </div>
  )
})

/**
 * One line's variants. A labelled group rather than a region: lines are not
 * page landmarks, and several of them show at once.
 */
function VariantLine(props: {
  line: ModelFamilyLine
  showHeader: boolean
  perfMap: ReadonlyMap<string, ModelPerfBadgeData>
  priceOptions: ModelPriceCellOptions
  onModelClick: (modelName: string) => void
}) {
  const { t } = useTranslation()
  const headingId = useId()
  const lineName = props.line.line || t('Default')
  const rows = (
    <ul className='divide-border/60 divide-y'>
      {props.line.variants.map((variant) => (
        <ModelVariantRow
          key={variant.model.model_name}
          variant={variant}
          perf={props.perfMap.get(variant.model.model_name)}
          priceOptions={props.priceOptions}
          onModelClick={props.onModelClick}
        />
      ))}
    </ul>
  )
  if (!props.showHeader) return rows

  return (
    <div role='group' aria-labelledby={headingId}>
      {/* Sticky so the line stays named while its variants scroll by. */}
      <div className='bg-muted sticky top-0 z-10 flex min-w-0 items-center justify-between gap-2 px-3 py-2 text-xs'>
        <h4
          id={headingId}
          className='text-foreground truncate text-sm font-bold'
          title={lineName}
        >
          {lineName}
        </h4>
        <span className='text-muted-foreground shrink-0 tabular-nums'>
          {t('Variants: {{count}}', { count: props.line.variants.length })}
        </span>
      </div>
      {rows}
    </div>
  )
}

function ModelVariantRow(props: {
  variant: ModelVariant
  perf: ModelPerfBadgeData | undefined
  priceOptions: ModelPriceCellOptions
  onModelClick: (modelName: string) => void
}) {
  const { t } = useTranslation()
  const name = props.variant.model.model_name
  const label = props.variant.parsed.label
  const description = props.variant.parsed.description

  return (
    <li className='flex min-w-0 flex-col gap-2 px-3 py-2.5 @3xl/variants:grid @3xl/variants:grid-cols-[minmax(10rem,1fr)_minmax(0,16rem)_auto_auto] @3xl/variants:items-center @3xl/variants:gap-x-5'>
      {/* Stacked layout: the price stays next to the label while both fit and
          drops to its own line otherwise; the label wraps, never truncates. */}
      <div className='flex min-w-0 flex-wrap items-baseline justify-between gap-x-3 gap-y-1 @3xl/variants:contents'>
        <div className='min-w-0 flex-[1_1_8rem]'>
          <p
            className={cn(
              'text-sm font-medium [overflow-wrap:anywhere]',
              label && 'font-mono text-[13px]'
            )}
            title={name}
          >
            {label || t('Standard')}
          </p>
          {description && (
            <p className='text-muted-foreground mt-0.5 text-xs leading-4 break-words'>
              {description}
            </p>
          )}
        </div>
        <VariantPrice
          model={props.variant.model}
          options={props.priceOptions}
        />
      </div>
      <div className='flex min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1.5 @3xl/variants:contents'>
        <ModelPerfBadge perf={props.perf} compact />
        <div className='ml-auto flex shrink-0 items-center gap-1'>
          <CopyButton
            value={name}
            tooltip={t('Copy model name')}
            aria-label={t('Copy {{field}}', { field: name })}
            className='size-7'
            iconClassName='size-3.5'
          />
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='h-7 gap-1 px-2'
            aria-label={`${t('Details')} ${name}`}
            onClick={() => props.onModelClick(name)}
          >
            <span className='sr-only @3xl/variants:not-sr-only'>
              {t('Details')}
            </span>
            <ChevronRight aria-hidden className='size-3.5' />
          </Button>
        </div>
      </div>
    </li>
  )
}
