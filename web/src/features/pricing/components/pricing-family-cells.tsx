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
import type { Row } from '@tanstack/react-table'
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import { getBillingModeLabelKey } from '../lib/billing-mode'
import type { ModelFamily } from '../lib/model-variants'
import type { PricingModel } from '../types'
import { ModelBillingModeBadge } from './model-billing-mode-badge'

/** A pricing table row: a model, or the parent row of a variant family. */
export type PricingTableRow = PricingModel & { family?: ModelFamily }

/** Name cell of a family parent row: expand toggle, base name, count. */
export function FamilyNameCell(props: {
  row: Row<PricingTableRow>
  family: ModelFamily
}) {
  const { t } = useTranslation()
  const expanded = props.row.getIsExpanded()
  const lead = props.family.variants[0].model
  const iconKey = lead.icon || lead.vendor_icon
  return (
    <div className='flex max-w-full min-w-0 items-center gap-2'>
      <Button
        type='button'
        variant='ghost'
        size='icon-sm'
        className='-ml-1.5 size-6 shrink-0'
        aria-expanded={expanded}
        aria-label={`${expanded ? t('Collapse') : t('Expand')} ${props.family.base}`}
        onClick={(event) => {
          event.stopPropagation()
          props.row.toggleExpanded()
        }}
      >
        <ChevronRight
          aria-hidden
          className={cn(
            'size-3.5 transition-transform',
            expanded && 'rotate-90'
          )}
        />
      </Button>
      {iconKey ? getLobeIcon(iconKey, 14) : null}
      <span
        className='truncate font-mono text-sm font-semibold'
        title={props.family.base}
      >
        {props.family.base}
      </span>
      <Badge variant='secondary' className='font-normal tabular-nums'>
        {t('Variants: {{count}}', { count: props.family.variants.length })}
      </Badge>
    </div>
  )
}

/** Distinct billing modes of a family, one badge each. */
export function FamilyBillingModes(props: { family: ModelFamily }) {
  const seen = new Set<string>()
  const models = props.family.variants
    .map((variant) => variant.model)
    .filter((model) => {
      const key = getBillingModeLabelKey(model)
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
  return (
    <div className='-ml-1.5 flex min-w-0 flex-wrap gap-1'>
      {models.map((model) => (
        <ModelBillingModeBadge key={model.model_name} model={model} />
      ))}
    </div>
  )
}
