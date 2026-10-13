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
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import {
  memo,
  useCallback,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { getPerfMetricsSummary } from '@/features/performance-metrics/api'
import { useMediaQuery } from '@/hooks/use-media-query'
import { requireServerSuccess } from '@/lib/server-error-message'

import { DEFAULT_PRICING_PAGE_SIZE, DEFAULT_TOKEN_UNIT } from '../constants'
import type { ModelFamily } from '../lib/model-variants'
import type { PricingModel, TokenUnit } from '../types'
import { ModelCard } from './model-card'
import { ModelFamilyCard } from './model-family-card'
import { ModelFamilyPanel } from './model-family-panel'
import type { ModelPerfBadgeData } from './model-perf-badge'

export interface ModelCardGridProps {
  models: PricingModel[]
  /**
   * Variant families of `models`. When set, the grid pages by family and
   * renders one card per family; single-member families stay plain cards.
   */
  families?: ModelFamily[]
  onModelClick: (modelName: string) => void
  priceRate?: number
  usdExchangeRate?: number
  tokenUnit?: TokenUnit
  showRechargePrice?: boolean
  selectedGroup?: string
}

/** Columns of the card grid; mirrors `md:grid-cols-2 xl:grid-cols-3`. */
function useCardGridColumns(): number {
  const isMedium = useMediaQuery('(min-width: 48rem)')
  const isWide = useMediaQuery('(min-width: 80rem)')
  if (isWide) return 3
  return isMedium ? 2 : 1
}

/**
 * The open family and the list and page it was opened on. One family is open
 * at a time; any change of the family list (search, filters, sort) or of the
 * page closes it.
 */
type Expansion = { families: ModelFamily[]; page: number; key: string }

export const ModelCardGrid = memo(function ModelCardGrid(
  props: ModelCardGridProps
) {
  const { t } = useTranslation()
  const columns = useCardGridColumns()
  const [page, setPage] = useState(1)
  const [expansion, setExpansion] = useState<Expansion | null>(null)
  const pageSize = DEFAULT_PRICING_PAGE_SIZE
  const tokenUnit = props.tokenUnit ?? DEFAULT_TOKEN_UNIT
  const itemCount = props.families?.length ?? props.models.length
  const totalPages = Math.max(1, Math.ceil(itemCount / pageSize))
  const currentPage = Math.min(page, totalPages)
  const expandedKey =
    expansion?.families === props.families && expansion?.page === currentPage
      ? expansion.key
      : null
  const panelId = useId()

  const perfQuery = useQuery({
    queryKey: ['perf-metrics-summary', 24],
    queryFn: async () => requireServerSuccess(await getPerfMetricsSummary(24)),
    staleTime: 60 * 1000,
    retry: false,
  })

  const pagedModels = useMemo(() => {
    const start = (currentPage - 1) * pageSize
    return props.models.slice(start, start + pageSize)
  }, [currentPage, pageSize, props.models])

  const pagedFamilies = useMemo(() => {
    const start = (currentPage - 1) * pageSize
    return props.families?.slice(start, start + pageSize)
  }, [currentPage, pageSize, props.families])

  const priceOptions = useMemo(
    () => ({
      tokenUnit,
      priceRate: props.priceRate,
      usdExchangeRate: props.usdExchangeRate,
      showRechargePrice: props.showRechargePrice,
      selectedGroup: props.selectedGroup,
    }),
    [
      tokenUnit,
      props.priceRate,
      props.usdExchangeRate,
      props.showRechargePrice,
      props.selectedGroup,
    ]
  )

  const perfMap = useMemo(() => {
    const map = new Map<string, ModelPerfBadgeData>()
    for (const model of perfQuery.data?.data?.models ?? []) {
      map.set(model.model_name, {
        ...model,
        window_start: perfQuery.data?.data.window_start,
        window_end: perfQuery.data?.data.window_end,
      })
    }
    return map
  }, [perfQuery.data])

  const expandedToggleRef = useRef<HTMLButtonElement>(null)

  const handleExpandedChange = useCallback(
    (familyKey: string, expanded: boolean) => {
      setExpansion(
        expanded && props.families
          ? { families: props.families, page: currentPage, key: familyKey }
          : null
      )
    },
    [props.families, currentPage]
  )

  // The card stays mounted while its panel closes, so focus can return to it.
  const handleCollapse = useCallback(() => {
    expandedToggleRef.current?.focus()
    setExpansion(null)
  }, [])

  const goToPage = (nextPage: number) => {
    setPage(nextPage)
    setExpansion(null)
  }

  const renderModelCard = (model: PricingModel) => (
    <ModelCard
      key={model.id ?? model.model_name}
      model={model}
      tokenUnit={tokenUnit}
      priceRate={props.priceRate}
      usdExchangeRate={props.usdExchangeRate}
      showRechargePrice={props.showRechargePrice}
      selectedGroup={props.selectedGroup}
      perf={perfMap.get(model.model_name || '')}
      onClick={props.onModelClick}
    />
  )

  const renderFamily = (family: ModelFamily) => {
    if (family.models.length === 1) return renderModelCard(family.models[0])
    return (
      <ModelFamilyCard
        key={`family:${family.key}`}
        family={family}
        expanded={family.key === expandedKey}
        panelId={panelId}
        toggleRef={family.key === expandedKey ? expandedToggleRef : undefined}
        onExpandedChange={handleExpandedChange}
        perfMap={perfMap}
        priceOptions={priceOptions}
      />
    )
  }

  const cards: ReactNode[] = pagedFamilies
    ? pagedFamilies.map(renderFamily)
    : pagedModels.map(renderModelCard)

  // The variant panel spans the grid after the last card of the expanded
  // card's row, so no card moves and no cell stays empty.
  const expandedIndex =
    pagedFamilies?.findIndex((family) => family.key === expandedKey) ?? -1
  if (pagedFamilies && expandedIndex >= 0) {
    const rowEnd = Math.min(
      pagedFamilies.length,
      (Math.floor(expandedIndex / columns) + 1) * columns
    )
    const family = pagedFamilies[expandedIndex]
    cards.splice(
      rowEnd,
      0,
      <ModelFamilyPanel
        key={`panel:${family.key}`}
        id={panelId}
        family={family}
        column={expandedIndex % columns}
        columns={columns}
        perfMap={perfMap}
        priceOptions={priceOptions}
        onModelClick={props.onModelClick}
        onCollapse={handleCollapse}
      />
    )
  }

  if (props.models.length === 0) {
    return null
  }

  return (
    <div className='flex flex-col gap-4 sm:gap-5'>
      <div className='grid grid-cols-1 gap-(--pricing-grid-gap) [--pricing-grid-gap:0.75rem] sm:[--pricing-grid-gap:1rem] md:grid-cols-2 xl:grid-cols-3'>
        {cards}
      </div>

      {totalPages > 1 && (
        <div className='text-muted-foreground flex flex-col items-center justify-between gap-3 border-t px-4 py-3 text-sm sm:flex-row'>
          <p className='text-muted-foreground'>
            {t('Page {{current}} of {{total}}', {
              current: currentPage,
              total: totalPages,
            })}
          </p>
          <div className='flex items-center gap-2'>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => goToPage(Math.max(1, currentPage - 1))}
              disabled={currentPage <= 1}
              className='gap-1.5'
            >
              <ChevronLeft className='size-4' />
              {t('Previous page')}
            </Button>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => goToPage(Math.min(totalPages, currentPage + 1))}
              disabled={currentPage >= totalPages}
              className='gap-1.5'
            >
              {t('Next page')}
              <ChevronRight className='size-4' />
            </Button>
          </div>
        </div>
      )}
    </div>
  )
})
