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
import { memo, useId, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import {
  getFamilyTags,
  getLineDisplayName,
  getLineGroups,
  getModelPriceGroups,
} from '../lib/model-family'
import type {
  ModelFamily,
  ModelFamilyLine,
  ModelVariant,
} from '../lib/model-variants'
import { VariantGroupPrices } from './model-family-price'
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
  const selectedGroup = props.priceOptions.selectedGroup
  const lines = useMemo(
    () =>
      props.family.lines.map((line) => ({
        line,
        groups: getLineGroups(line, selectedGroup),
      })),
    [props.family, selectedGroup]
  )
  const familyTags = useMemo(() => getFamilyTags(props.family), [props.family])
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
      {lines.map(({ line, groups }) => (
        <VariantLine
          key={line.line}
          line={line}
          groups={groups}
          familyTags={familyTags}
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
  /** Groups the line's variants are priced in, cheapest first. */
  groups: string[]
  familyTags: ReadonlySet<string>
  showHeader: boolean
  perfMap: ReadonlyMap<string, ModelPerfBadgeData>
  priceOptions: ModelPriceCellOptions
  onModelClick: (modelName: string) => void
}) {
  const { t } = useTranslation()
  const headingId = useId()
  const lineName = getLineDisplayName(
    t,
    props.line.line,
    props.groups,
    props.familyTags
  )
  // A tag that is not itself the group also names the groups behind it.
  const showGroups =
    props.line.line !== '' &&
    props.groups.length > 0 &&
    !(props.groups.length === 1 && props.groups[0] === props.line.line)
  // A row names its groups only when the header does not already say them:
  // the line spans several groups and this variant is not in all of them.
  const labelsGroups = (variant: ModelVariant) => {
    if (props.groups.length < 2) return false
    if (!props.showHeader) return true
    const groups = getModelPriceGroups(
      variant.model,
      props.priceOptions.selectedGroup
    )
    return (
      groups.length !== props.groups.length ||
      groups.some((group) => !props.groups.includes(group))
    )
  }
  const rows = (
    <ul className='divide-border/60 divide-y'>
      {props.line.variants.map((variant) => (
        <ModelVariantRow
          key={variant.model.model_name}
          variant={variant}
          labelGroups={labelsGroups(variant)}
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
        <div className='flex min-w-0 items-baseline gap-2'>
          <h4
            id={headingId}
            className='text-foreground truncate text-sm font-bold'
            title={lineName}
          >
            {lineName}
          </h4>
          {showGroups && (
            <span
              className='text-muted-foreground truncate'
              title={props.groups.join(', ')}
            >
              {t('Group')} {props.groups.join(', ')}
            </span>
          )}
        </div>
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
  /** Name the group of every price: the line spans several groups. */
  labelGroups: boolean
  perf: ModelPerfBadgeData | undefined
  priceOptions: ModelPriceCellOptions
  onModelClick: (modelName: string) => void
}) {
  const { t } = useTranslation()
  const name = props.variant.model.model_name
  const label = props.variant.parsed.label
  const description = props.variant.parsed.description

  return (
    <li className='flex min-w-0 flex-col gap-2 px-3 py-2.5 @3xl/variants:grid @3xl/variants:grid-cols-[minmax(10rem,1fr)_minmax(0,22rem)_auto_auto] @3xl/variants:items-center @3xl/variants:gap-x-5'>
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
        <VariantGroupPrices
          model={props.variant.model}
          options={props.priceOptions}
          labelGroups={props.labelGroups}
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
