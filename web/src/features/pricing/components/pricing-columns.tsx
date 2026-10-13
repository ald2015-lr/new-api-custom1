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
import type { ColumnDef } from '@tanstack/react-table'
import type { TFunction } from 'i18next'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  BadgeCell,
  BadgeListCell,
  DataTableColumnHeader,
} from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import { parseTags } from '../lib/filters'
import type { GroupAccessRequirement } from '../types'
import { CachedPriceCell } from './cached-price-cell'
import { FreeGroupBadge } from './free-group-badge'
import { GroupAccessBadge } from './group-access-badge'
import { ModelBillingModeBadge } from './model-billing-mode-badge'
import { FamilyLinePrices } from './model-family-price'
import { ModelPriceCell, type ModelPriceCellOptions } from './model-price-cell'
import {
  FamilyBillingModes,
  FamilyNameCell,
  type PricingTableRow,
} from './pricing-family-cells'

// ----------------------------------------------------------------------------
// Pricing Table Columns
// ----------------------------------------------------------------------------

export type PricingColumnsOptions = ModelPriceCellOptions & {
  /** Recharge-gated groups, marked next to the group name. */
  groupAccess?: Record<string, GroupAccessRequirement>
  /** Groups that do not charge users, marked next to the group name. */
  freeGroups?: readonly string[]
}

export function usePricingColumns(
  columnOptions: PricingColumnsOptions = {}
): ColumnDef<PricingTableRow>[] {
  const { t } = useTranslation()
  const {
    tokenUnit,
    priceRate,
    usdExchangeRate,
    showRechargePrice,
    selectedGroup,
    groupAccess,
    freeGroups,
  } = columnOptions

  // Stable column definitions keep cell components mounted across row
  // updates (e.g. expanding a family keeps focus on its toggle) and let rows
  // re-render only when a display option really changes.
  return useMemo(() => {
    const options: PricingColumnsOptions = {
      tokenUnit,
      priceRate,
      usdExchangeRate,
      showRechargePrice,
      selectedGroup,
      groupAccess,
      freeGroups,
    }
    return buildPricingColumns(t, options)
  }, [
    t,
    tokenUnit,
    priceRate,
    usdExchangeRate,
    showRechargePrice,
    selectedGroup,
    groupAccess,
    freeGroups,
  ])
}

function buildPricingColumns(
  t: TFunction,
  options: PricingColumnsOptions
): ColumnDef<PricingTableRow>[] {
  return [
    // Model column
    {
      accessorKey: 'model_name',
      meta: { label: t('Model') },
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Model')} />
      ),
      cell: ({ row }) => {
        const model = row.original
        if (model.family) {
          return <FamilyNameCell row={row} family={model.family} />
        }
        const modelIconKey = model.icon || model.vendor_icon
        const modelIcon = modelIconKey ? getLobeIcon(modelIconKey, 14) : null

        return (
          <div
            className={cn(
              'flex max-w-full min-w-0 items-center gap-2',
              // Variant rows sit under their family's expand toggle.
              row.depth > 0 && 'pl-6'
            )}
          >
            {modelIcon}
            <span className='truncate font-mono text-sm font-medium'>
              {model.model_name}
            </span>
          </div>
        )
      },
      minSize: 200,
    },

    // Type column
    {
      accessorKey: 'quota_type',
      header: t('Type'),
      cell: ({ row }) =>
        row.original.family ? (
          <FamilyBillingModes family={row.original.family} />
        ) : (
          <ModelBillingModeBadge model={row.original} className='-ml-1.5' />
        ),
      size: 110,
      enableSorting: false,
    },

    // Price column
    {
      accessorKey: 'price',
      meta: { label: t('Price') },
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Price')} />
      ),
      cell: ({ row }) =>
        row.original.family ? (
          <FamilyLinePrices
            family={row.original.family}
            options={options}
            layout='list'
          />
        ) : (
          <ModelPriceCell model={row.original} options={options} />
        ),
      size: 180,
      enableSorting: false,
    },

    // Cached price column (Vercel AI Gateway style)
    {
      id: 'cached_price',
      header: t('Cached'),
      cell: ({ row }) =>
        row.original.family ? (
          <span className='text-muted-foreground/30 text-xs'>—</span>
        ) : (
          <CachedPriceCell model={row.original} options={options} />
        ),
      size: 110,
      enableSorting: false,
    },

    // Vendor column
    {
      accessorKey: 'vendor_name',
      header: t('Vendor'),
      cell: ({ row }) => {
        const model = row.original
        if (!model.vendor_name) {
          return <span className='text-muted-foreground/50 text-xs'>—</span>
        }
        const vendorIcon = model.vendor_icon
          ? getLobeIcon(model.vendor_icon, 12)
          : null
        return (
          <BadgeCell className='gap-1.5'>
            {vendorIcon}
            <StatusBadge
              label={model.vendor_name}
              autoColor={model.vendor_name}
              size='sm'
              copyable={false}
            />
          </BadgeCell>
        )
      },
      size: 130,
      enableSorting: false,
    },

    // Tags column
    {
      accessorKey: 'tags',
      header: t('Tags'),
      cell: ({ row }) => {
        const tags = parseTags(row.original.tags)
        return (
          <BadgeListCell
            items={tags.map((tag) => (
              <StatusBadge
                key={tag}
                label={tag}
                autoColor={tag}
                size='sm'
                copyable={false}
              />
            ))}
          />
        )
      },
      size: 140,
      enableSorting: false,
    },

    // Endpoints column
    {
      accessorKey: 'supported_endpoint_types',
      header: t('Endpoints'),
      cell: ({ row }) => {
        const endpoints = row.original.supported_endpoint_types || []
        return (
          <BadgeListCell
            items={endpoints.map((ep) => (
              <StatusBadge
                key={ep}
                label={ep}
                autoColor={ep}
                size='sm'
                copyable={false}
              />
            ))}
          />
        )
      },
      size: 130,
      enableSorting: false,
    },

    // Enable Groups column
    {
      accessorKey: 'enable_groups',
      header: t('Groups'),
      cell: ({ row }) => {
        const groups = row.original.enable_groups || []
        return (
          <BadgeListCell
            items={groups.map((group) => (
              <span
                key={group}
                className='inline-flex min-w-0 shrink-0 items-center gap-1'
              >
                <GroupBadge group={group} size='sm' />
                <GroupAccessBadge requirement={options.groupAccess?.[group]} />
                <FreeGroupBadge group={group} freeGroups={options.freeGroups} />
              </span>
            ))}
            tooltipClassName='max-w-[280px] p-2'
          />
        )
      },
      size: 130,
      enableSorting: false,
    },
  ]
}
