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
import { ChevronUp } from 'lucide-react'
import { memo, useEffect, useId, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import type { ModelFamily } from '../lib/model-variants'
import type { ModelPerfBadgeData } from './model-perf-badge'
import type { ModelPriceCellOptions } from './model-price-cell'
import { ModelVariantList } from './model-variant-list'

export interface ModelFamilyPanelProps {
  id: string
  family: ModelFamily
  /** Column of the expanded card (0-based) and the grid's column count. */
  column: number
  columns: number
  perfMap: ReadonlyMap<string, ModelPerfBadgeData>
  priceOptions: ModelPriceCellOptions
  onModelClick: (modelName: string) => void
  onCollapse: () => void
}

function prefersReducedMotion(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true
  )
}

/**
 * Full-width grid item with a family's variants, placed after the row of the
 * expanded card. It takes focus when it opens (so the next Tab reaches the
 * first variant) and scrolls into view below the fixed site header.
 */
export const ModelFamilyPanel = memo(function ModelFamilyPanel(
  props: ModelFamilyPanelProps
) {
  const { t } = useTranslation()
  const headingId = useId()
  const panelRef = useRef<HTMLDivElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)

  // The panel mounts when its family expands (it is keyed by family).
  useEffect(() => {
    headingRef.current?.focus({ preventScroll: true })
    panelRef.current?.scrollIntoView?.({
      block: 'nearest',
      behavior: prefersReducedMotion() ? 'auto' : 'smooth',
    })
  }, [])

  // Points at the expanded card: the centre of its column, gaps included.
  const pointerLeft = `calc((100% - ${props.columns - 1} * var(--pricing-grid-gap)) / ${props.columns} * ${props.column + 0.5} + ${props.column} * var(--pricing-grid-gap))`

  return (
    <div
      ref={panelRef}
      id={props.id}
      role='region'
      aria-labelledby={headingId}
      className='bg-card text-card-foreground ring-foreground/10 relative col-span-full min-w-0 scroll-mt-20 scroll-mb-4 rounded-xl p-3 shadow-sm ring-1 sm:p-4'
    >
      {props.columns > 1 && (
        <span
          aria-hidden
          data-slot='panel-pointer'
          className='bg-card border-foreground/10 absolute -top-[7px] size-3.5 -translate-x-1/2 rotate-45 rounded-tl-[3px] border-t border-l'
          style={{ left: pointerLeft }}
        />
      )}
      <div className='mb-3 flex min-w-0 items-center justify-between gap-3'>
        <h3
          ref={headingRef}
          id={headingId}
          tabIndex={-1}
          className='focus-visible:ring-ring/50 flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 rounded-sm outline-none focus-visible:ring-2'
        >
          <span className='font-mono text-sm font-semibold [overflow-wrap:anywhere]'>
            {props.family.base}
          </span>{' '}
          <span className='text-muted-foreground text-xs font-normal tabular-nums'>
            {t('Variants: {{count}}', { count: props.family.variants.length })}
          </span>
        </h3>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          className='shrink-0'
          aria-label={`${t('Collapse')} ${props.family.base}`}
          onClick={props.onCollapse}
        >
          {t('Collapse')}
          <ChevronUp aria-hidden className='size-3.5' />
        </Button>
      </div>
      <ModelVariantList
        family={props.family}
        perfMap={props.perfMap}
        priceOptions={props.priceOptions}
        onModelClick={props.onModelClick}
      />
    </div>
  )
})
