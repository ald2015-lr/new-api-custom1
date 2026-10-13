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
import type {
  ExpandedState,
  OnChangeFn,
  PaginationState,
  Row,
} from '@tanstack/react-table'
import { useState, useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePagination,
  DataTableRow,
  DataTableView,
  useDataTable,
} from '@/components/data-table'
import { cn } from '@/lib/utils'

import { DEFAULT_PRICING_PAGE_SIZE, DEFAULT_TOKEN_UNIT } from '../constants'
import { getFamilyUnion } from '../lib/model-family'
import type { ModelFamily } from '../lib/model-variants'
import type { GroupAccessRequirement, PricingModel, TokenUnit } from '../types'
import { usePricingColumns } from './pricing-columns'
import type { PricingTableRow } from './pricing-family-cells'

/**
 * Parent row of a family: the lead variant's vendor and icon with the union
 * of the members' tags, endpoints and groups. Single-member families stay
 * plain model rows.
 */
function toFamilyRow(family: ModelFamily): PricingTableRow {
  if (family.models.length === 1) return family.models[0]
  const union = getFamilyUnion(family)
  return {
    ...family.variants[0].model,
    model_name: family.base,
    tags: union.tags.join(','),
    supported_endpoint_types: union.endpoints,
    enable_groups: union.groups,
    family,
  }
}

function getFamilySubRows(row: PricingTableRow): PricingTableRow[] | undefined {
  return row.family?.variants.map((variant) => variant.model)
}

function getFamilyRowId(
  row: PricingTableRow,
  _index: number,
  parent?: Row<PricingTableRow>
): string {
  if (parent) return `${parent.id}/${row.model_name}`
  return row.family ? `family:${row.family.key}` : `model:${row.model_name}`
}

export interface PricingTableProps {
  models: PricingModel[]
  /**
   * Variant families of `models`. When set, the table pages by family and
   * shows one expandable parent row per family with its variants as children.
   */
  families?: ModelFamily[]
  isLoading?: boolean
  priceRate?: number
  usdExchangeRate?: number
  tokenUnit?: TokenUnit
  showRechargePrice?: boolean
  selectedGroup?: string
  groupAccess?: Record<string, GroupAccessRequirement>
  freeGroups?: readonly string[]
  onModelClick?: (modelName: string) => void
}

export function PricingTable(props: PricingTableProps) {
  const { t } = useTranslation()
  const {
    models,
    isLoading = false,
    priceRate = 1,
    usdExchangeRate = 1,
    tokenUnit = DEFAULT_TOKEN_UNIT,
    showRechargePrice = false,
    selectedGroup,
    onModelClick,
  } = props

  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: DEFAULT_PRICING_PAGE_SIZE,
  })
  const [expanded, setExpanded] = useState<ExpandedState>({})
  const grouped = props.families !== undefined
  const rows = useMemo<PricingTableRow[]>(
    () => props.families?.map(toFamilyRow) ?? models,
    [props.families, models]
  )
  // Expansion is per page, as in the card grid.
  const handlePaginationChange = useCallback<OnChangeFn<PaginationState>>(
    (updater) => {
      setPagination(updater)
      setExpanded({})
    },
    []
  )

  const columns = usePricingColumns({
    tokenUnit,
    priceRate,
    usdExchangeRate,
    showRechargePrice,
    selectedGroup,
    groupAccess: props.groupAccess,
    freeGroups: props.freeGroups,
  })

  const { table } = useDataTable<PricingTableRow>({
    data: rows,
    columns,
    pageCount: Math.ceil(rows.length / pagination.pageSize),
    pagination,
    onPaginationChange: grouped ? handlePaginationChange : setPagination,
    manualPagination: false,
    withFilteredRowModel: false,
    withSortedRowModel: false,
    withFacetedRowModel: false,
    ...(grouped
      ? {
          expanded,
          onExpandedChange: setExpanded,
          getSubRows: getFamilySubRows,
          getRowId: getFamilyRowId,
          withExpandedRowModel: true,
          paginateExpandedRows: false,
        }
      : {}),
  })

  const handleRowClick = useCallback(
    (row: Row<PricingTableRow>) => {
      if (row.original.family) {
        row.toggleExpanded()
        return
      }
      onModelClick?.(row.original.model_name)
    },
    [onModelClick]
  )

  return (
    <div className='space-y-4'>
      <DataTableView
        table={table}
        isLoading={isLoading}
        emptyTitle={t('No Models Found')}
        emptyDescription={t('No models match your current filters.')}
        skeletonKeyPrefix='pricing-skeleton'
        applyHeaderSize
        getColumnClassName={(_columnId, kind) =>
          kind === 'header' ? 'text-muted-foreground font-medium' : undefined
        }
        renderRow={(row: Row<PricingTableRow>) => (
          <DataTableRow
            key={row.id}
            row={row}
            className={cn(
              'hover:bg-muted/30 cursor-pointer transition-colors',
              row.depth > 0 && 'bg-muted/15',
              row.original.family && row.getIsExpanded() && 'bg-muted/25'
            )}
            cellRenderColumns={table.options.columns}
            onClick={() => handleRowClick(row)}
          />
        )}
      />

      {!isLoading && models.length > 0 && <DataTablePagination table={table} />}
    </div>
  )
}
