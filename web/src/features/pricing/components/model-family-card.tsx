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
import { ChevronDown } from 'lucide-react'
import { memo, useMemo, type Ref } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import { parseTags } from '../lib/filters'
import { aggregateModelPerf, getFamilyUnion } from '../lib/model-family'
import type { ModelFamily } from '../lib/model-variants'
import { FamilyLinePrices } from './model-family-price'
import { ModelGroupsEndpoints } from './model-groups-endpoints'
import { ModelPerfBadge, type ModelPerfBadgeData } from './model-perf-badge'
import type { ModelPriceCellOptions } from './model-price-cell'

/** Variant chips shown on a card before the `+N` overflow. */
const MAX_VARIANT_CHIPS = 4

export interface ModelFamilyCardProps {
  family: ModelFamily
  /** Whether this family's variant panel is open below the card's row. */
  expanded: boolean
  /** Id of the variant panel, referenced while it is open. */
  panelId: string
  /** Receives the expand toggle, so a closing panel can hand focus back. */
  toggleRef?: Ref<HTMLButtonElement>
  onExpandedChange: (familyKey: string, expanded: boolean) => void
  perfMap: ReadonlyMap<string, ModelPerfBadgeData>
  priceOptions: ModelPriceCellOptions
}

/**
 * One card per base model, summarizing its lines (lowest price each), variant
 * labels, groups and endpoints. The card keeps its grid cell when expanded:
 * the grid renders the variant panel after the card's row. Model names are
 * only regrouped for display, never rewritten.
 */
export const ModelFamilyCard = memo(function ModelFamilyCard(
  props: ModelFamilyCardProps
) {
  const { t } = useTranslation()
  const family = props.family
  const lead = family.variants[0].model
  const describedVariant = family.variants.find(
    (variant) => variant.model.description
  )
  const taggedVariant = family.variants.find((variant) => variant.model.tags)
  const tags = parseTags(taggedVariant?.model.tags)
  const iconKey = lead.icon || lead.vendor_icon
  const icon = iconKey ? getLobeIcon(iconKey, 28) : null
  const hiddenLabels = family.variantLabels.slice(MAX_VARIANT_CHIPS)
  const union = useMemo(() => getFamilyUnion(family), [family])
  const perf = useMemo(
    () =>
      aggregateModelPerf(
        family.models.map((model) => props.perfMap.get(model.model_name))
      ),
    [family.models, props.perfMap]
  )

  const labelText = (label: string) => label || t('Standard')
  const toggleText = props.expanded ? t('Collapse') : t('Expand')

  return (
    <Card
      data-expanded={props.expanded || undefined}
      // The global hover lift replaces the ring; keep the open state visible.
      data-card-hover={props.expanded ? 'false' : undefined}
      className={cn(
        'min-w-0 gap-3 transition-shadow',
        props.expanded ? 'ring-primary/60 ring-2' : 'hover:ring-foreground/20'
      )}
    >
      <CardHeader className='flex flex-row items-start gap-3'>
        <div
          aria-hidden
          className='bg-muted/50 flex size-10 shrink-0 items-center justify-center rounded-lg'
        >
          {icon || (
            <span className='text-muted-foreground text-sm font-bold'>
              {family.base.charAt(0).toUpperCase() || '?'}
            </span>
          )}
        </div>
        <div className='min-w-0 flex-1'>
          <h3
            className='line-clamp-2 font-mono text-[15px] leading-snug font-semibold [overflow-wrap:anywhere]'
            title={family.base}
          >
            {family.base}
          </h3>
          {lead.vendor_name && (
            <p
              className='text-muted-foreground mt-1 truncate text-xs'
              title={lead.vendor_name}
            >
              {lead.vendor_name}
            </p>
          )}
        </div>
      </CardHeader>
      <CardContent className='flex flex-1 flex-col gap-3'>
        <div className='flex min-w-0 flex-col gap-1.5'>
          <p className='text-muted-foreground line-clamp-2 text-[13px] leading-5 break-words'>
            {describedVariant?.model.description ||
              t('No description available.')}
          </p>
          {tags.length > 0 && (
            <div
              role='group'
              aria-label={t('Tags')}
              className='text-muted-foreground flex min-w-0 items-baseline gap-1.5 text-xs'
            >
              <span className='shrink-0'>{t('Tags')}</span>
              <span className='truncate' title={tags.join(', ')}>
                {tags.slice(0, 2).join(', ')}
              </span>
              {tags.length > 2 && (
                <span className='shrink-0' title={tags.slice(2).join(', ')}>
                  +{tags.length - 2}
                </span>
              )}
            </div>
          )}
        </div>

        <div className='mt-auto flex min-w-0 flex-col gap-2.5'>
          <div
            role='group'
            aria-label={t('Lines')}
            className='flex min-w-0 flex-col gap-1.5'
          >
            <span className='text-muted-foreground text-xs'>{t('Lines')}</span>
            <FamilyLinePrices
              family={family}
              options={props.priceOptions}
              layout='chips'
            />
          </div>
          <div
            role='group'
            aria-label={t('Variants')}
            className='flex min-w-0 flex-col gap-1.5'
          >
            <span className='text-muted-foreground text-xs'>
              {t('Variants')}
            </span>
            <ul className='flex min-w-0 flex-wrap gap-1.5'>
              {family.variantLabels.slice(0, MAX_VARIANT_CHIPS).map((label) => (
                <li key={label} className='max-w-full min-w-0'>
                  <Badge
                    variant='secondary'
                    className={cn(
                      'max-w-full font-normal',
                      label && 'font-mono'
                    )}
                  >
                    <span className='truncate'>{labelText(label)}</span>
                  </Badge>
                </li>
              ))}
              {hiddenLabels.length > 0 && (
                <li>
                  <Badge
                    variant='outline'
                    className='text-muted-foreground font-normal tabular-nums'
                    title={hiddenLabels.map(labelText).join(', ')}
                  >
                    +{hiddenLabels.length}
                  </Badge>
                </li>
              )}
            </ul>
          </div>
        </div>

        <ModelGroupsEndpoints
          groups={union.groups}
          endpoints={union.endpoints}
        />

        <div className='flex min-w-0 items-center justify-between gap-2'>
          <span className='text-muted-foreground text-xs tabular-nums'>
            {t('Variants: {{count}}', { count: family.variants.length })}
          </span>
          <Button
            ref={props.toggleRef}
            type='button'
            variant={props.expanded ? 'secondary' : 'ghost'}
            size='sm'
            aria-expanded={props.expanded}
            aria-controls={props.expanded ? props.panelId : undefined}
            aria-label={`${toggleText} ${family.base}`}
            onClick={() => props.onExpandedChange(family.key, !props.expanded)}
          >
            {toggleText}
            <ChevronDown
              aria-hidden
              className={cn(
                'size-3.5 transition-transform',
                props.expanded && 'rotate-180'
              )}
            />
          </Button>
        </div>
      </CardContent>
      <CardFooter className='mt-auto border-0 bg-transparent pt-0'>
        <ModelPerfBadge
          perf={perf}
          className='border-border/60 border-t pt-2'
        />
      </CardFooter>
    </Card>
  )
})
