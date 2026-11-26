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
import {
  type ColumnDef,
  type ColumnFiltersState,
  type ColumnSizingState,
  type ExpandedState,
  type OnChangeFn,
  type PaginationState,
  type RowSelectionState,
  type SortingState,
  type TableOptions,
  type Updater,
  type VisibilityState,
  getCoreRowModel,
  getExpandedRowModel,
  getFacetedRowModel,
  getFacetedUniqueValues,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
} from '@tanstack/react-table'
import * as React from 'react'

type DataTableFeatureOptions<TData> = Pick<
  TableOptions<TData>,
  | 'enableRowSelection'
  | 'getRowId'
  | 'getSubRows'
  | 'paginateExpandedRows'
  | 'globalFilterFn'
  | 'autoResetPageIndex'
  | 'manualFiltering'
  | 'manualPagination'
  | 'manualSorting'
  | 'enableSorting'
  | 'enableColumnResizing'
>

type DataTableStateOptions = {
  initialSorting?: SortingState
  sorting?: SortingState
  onSortingChange?: OnChangeFn<SortingState>
  initialColumnVisibility?: VisibilityState
  columnVisibilityStorageKey?: string | false
  columnVisibility?: VisibilityState
  onColumnVisibilityChange?: OnChangeFn<VisibilityState>
  initialColumnSizing?: ColumnSizingState
  columnSizingStorageKey?: string | false
  columnSizing?: ColumnSizingState
  onColumnSizingChange?: OnChangeFn<ColumnSizingState>
  initialRowSelection?: RowSelectionState
  rowSelection?: RowSelectionState
  onRowSelectionChange?: OnChangeFn<RowSelectionState>
  initialExpanded?: ExpandedState
  expanded?: ExpandedState
  onExpandedChange?: OnChangeFn<ExpandedState>
  columnFilters?: ColumnFiltersState
  onColumnFiltersChange?: OnChangeFn<ColumnFiltersState>
  globalFilter?: string
  onGlobalFilterChange?: OnChangeFn<string>
  initialPagination?: PaginationState
  pagination?: PaginationState
  onPaginationChange?: OnChangeFn<PaginationState>
}

type DataTableRowModelOptions = {
  withFilteredRowModel?: boolean
  withPaginationRowModel?: boolean
  withSortedRowModel?: boolean
  withFacetedRowModel?: boolean
  withExpandedRowModel?: boolean
}

type UseDataTableOptions<TData> = DataTableFeatureOptions<TData> &
  DataTableStateOptions &
  DataTableRowModelOptions & {
    data: TData[]
    columns: ColumnDef<TData, unknown>[]
    totalCount?: number
    /**
     * The server stopped counting at totalCount: more rows match. The
     * server's page then decides the page count: a full page has a next page,
     * a short page is the last one. A page past the end comes back empty.
     * Paged onto from a full page, that page shows again, and as the last one
     * when it is the page right before (the matches fill a whole number of
     * pages); reached otherwise (an old link, another view, deleted rows),
     * the first page shows, as ensurePageInRange does for a counted total.
     * For server-paginated tables, where data holds the current page.
     */
    totalCountCapped?: boolean
    /**
     * data is a stand-in shown while the current page loads (react-query's
     * placeholderData, usually the previous page), not the server's answer
     * for the current pagination. A capped count then assumes another page
     * follows rather than reading the stand-in's length.
     */
    isPlaceholderData?: boolean
    pageCount?: number
    ensurePageInRange?: (
      pageCount: number,
      opts?: { resetTo?: 'first' | 'last' }
    ) => void
  }

type PagePosition = { pageIndex: number; pageSize: number }
type ServedPage = PagePosition & { rows: number }

function isSamePosition(a: PagePosition, b: PagePosition) {
  return a.pageIndex === b.pageIndex && a.pageSize === b.pageSize
}

type ColumnSizingBounds = Record<
  string,
  {
    minSize?: number
    maxSize?: number
  }
>

type ColumnWithSizing<TData> = ColumnDef<TData, unknown> & {
  accessorKey?: string | number
  columns?: ColumnDef<TData, unknown>[]
}

const COLUMN_SIZING_PERSIST_DELAY_MS = 250

function resolveUpdater<TValue>(
  updater: Updater<TValue>,
  previous: TValue
): TValue {
  return typeof updater === 'function'
    ? (updater as (old: TValue) => TValue)(previous)
    : updater
}

function useControllableTableState<TValue>(
  controlledValue: TValue | undefined,
  defaultValue: TValue,
  onChange: OnChangeFn<TValue> | undefined
): [TValue, OnChangeFn<TValue>] {
  const [uncontrolledValue, setUncontrolledValue] =
    React.useState<TValue>(defaultValue)

  const value = controlledValue ?? uncontrolledValue

  const setValue = React.useCallback<OnChangeFn<TValue>>(
    (updater) => {
      if (controlledValue === undefined) {
        setUncontrolledValue((previous) => resolveUpdater(updater, previous))
      }
      onChange?.(updater)
    },
    [controlledValue, onChange]
  )

  return [value, setValue]
}

function readColumnVisibility(storageKey: string | undefined): VisibilityState {
  if (!storageKey || typeof window === 'undefined') return {}

  try {
    const raw = window.localStorage.getItem(storageKey)
    if (!raw) return {}

    const parsed = JSON.parse(raw) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return {}
    }

    return Object.entries(parsed).reduce<VisibilityState>(
      (visibility, [key, value]) => {
        if (typeof value === 'boolean') {
          visibility[key] = value
        }
        return visibility
      },
      {}
    )
  } catch {
    return {}
  }
}

function getColumnId<TData>(column: ColumnDef<TData, unknown>) {
  const columnWithSizing = column as ColumnWithSizing<TData>

  if (typeof columnWithSizing.id === 'string') {
    return columnWithSizing.id
  }

  if (typeof columnWithSizing.accessorKey === 'string') {
    return columnWithSizing.accessorKey.replaceAll('.', '_')
  }

  if (typeof columnWithSizing.accessorKey === 'number') {
    return String(columnWithSizing.accessorKey)
  }

  return undefined
}

function buildColumnSizingBounds<TData>(
  columns: ColumnDef<TData, unknown>[]
): ColumnSizingBounds {
  return columns.reduce<ColumnSizingBounds>((bounds, column) => {
    const columnWithSizing = column as ColumnWithSizing<TData>
    const columnId = getColumnId(column)

    if (columnId) {
      const minSize =
        typeof columnWithSizing.minSize === 'number' &&
        Number.isFinite(columnWithSizing.minSize)
          ? columnWithSizing.minSize
          : undefined
      const maxSize =
        typeof columnWithSizing.maxSize === 'number' &&
        Number.isFinite(columnWithSizing.maxSize)
          ? columnWithSizing.maxSize
          : undefined

      if (minSize !== undefined || maxSize !== undefined) {
        bounds[columnId] = { minSize, maxSize }
      }
    }

    if (Array.isArray(columnWithSizing.columns)) {
      Object.assign(bounds, buildColumnSizingBounds(columnWithSizing.columns))
    }

    return bounds
  }, {})
}

function getBoundedColumnSize(
  columnId: string,
  value: unknown,
  bounds: ColumnSizingBounds
) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) {
    return undefined
  }

  const columnBounds = bounds[columnId]
  let size = value

  if (columnBounds?.minSize !== undefined && size < columnBounds.minSize) {
    size = columnBounds.minSize
  }

  if (columnBounds?.maxSize !== undefined && size > columnBounds.maxSize) {
    size = columnBounds.maxSize
  }

  return size > 0 ? size : undefined
}

function readColumnSizing(
  storageKey: string | undefined,
  bounds: ColumnSizingBounds
): ColumnSizingState {
  if (!storageKey || typeof window === 'undefined') return {}

  try {
    const raw = window.localStorage.getItem(storageKey)
    if (!raw) return {}

    const parsed = JSON.parse(raw) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return {}
    }

    return Object.entries(parsed).reduce<ColumnSizingState>(
      (sizing, [key, value]) => {
        const boundedSize = getBoundedColumnSize(key, value, bounds)

        if (boundedSize !== undefined) {
          sizing[key] = boundedSize
        }
        return sizing
      },
      {}
    )
  } catch {
    return {}
  }
}

export function useDataTable<TData>(options: UseDataTableOptions<TData>) {
  const {
    data,
    columns,
    totalCount,
    totalCountCapped = false,
    isPlaceholderData = false,
    pageCount: explicitPageCount,
    ensurePageInRange,
    manualFiltering,
    manualPagination,
    manualSorting,
    initialSorting = [],
    initialColumnVisibility = {},
    initialColumnSizing = {},
    initialRowSelection = {},
    initialExpanded = {},
    initialPagination = { pageIndex: 0, pageSize: 20 },
    withFilteredRowModel = !manualFiltering,
    withPaginationRowModel = !manualPagination,
    withSortedRowModel = !manualSorting && !manualPagination,
    withFacetedRowModel = !manualFiltering,
    withExpandedRowModel = false,
  } = options

  const columnVisibilityStorageKey =
    typeof options.columnVisibilityStorageKey === 'string'
      ? options.columnVisibilityStorageKey
      : undefined
  const columnSizingStorageKey =
    typeof options.columnSizingStorageKey === 'string'
      ? options.columnSizingStorageKey
      : undefined
  const resolvedInitialColumnVisibility = React.useMemo(
    () => ({
      ...initialColumnVisibility,
      ...readColumnVisibility(columnVisibilityStorageKey),
    }),
    [columnVisibilityStorageKey, initialColumnVisibility]
  )
  const columnSizingBounds = React.useMemo(
    () => buildColumnSizingBounds(columns),
    [columns]
  )
  const resolvedInitialColumnSizing = React.useMemo(
    () => ({
      ...initialColumnSizing,
      ...readColumnSizing(columnSizingStorageKey, columnSizingBounds),
    }),
    [columnSizingBounds, columnSizingStorageKey, initialColumnSizing]
  )

  const [sorting, onSortingChange] = useControllableTableState(
    options.sorting,
    initialSorting,
    options.onSortingChange
  )
  const [columnVisibility, onColumnVisibilityChange] =
    useControllableTableState(
      options.columnVisibility,
      resolvedInitialColumnVisibility,
      options.onColumnVisibilityChange
    )
  const [columnSizing, onColumnSizingChange] = useControllableTableState(
    options.columnSizing,
    resolvedInitialColumnSizing,
    options.onColumnSizingChange
  )
  const hydratedColumnVisibilityStorageKeyRef = React.useRef(
    columnVisibilityStorageKey
  )
  const hydratedColumnSizingStorageKeyRef = React.useRef(columnSizingStorageKey)
  const skipNextColumnVisibilityPersistRef = React.useRef(false)
  const skipNextColumnSizingPersistRef = React.useRef(false)
  const columnSizingPersistTimerRef = React.useRef<number | undefined>(
    undefined
  )
  const [rowSelection, onRowSelectionChange] = useControllableTableState(
    options.rowSelection,
    initialRowSelection,
    options.onRowSelectionChange
  )
  const [expanded, onExpandedChange] = useControllableTableState(
    options.expanded,
    initialExpanded,
    options.onExpandedChange
  )
  const [pagination, onPaginationChange] = useControllableTableState(
    options.pagination,
    initialPagination,
    options.onPaginationChange
  )

  // While the count is capped the end of the list is found by paging onto
  // it: servedPageRef is the last page the server answered, cappedLastPage a
  // full page that the empty page after it showed to be the last.
  const servedPageRef = React.useRef<ServedPage | undefined>(undefined)
  const [cappedLastPage, setCappedLastPage] =
    React.useState<PagePosition | null>(null)

  const countedPageCount =
    totalCount !== undefined
      ? Math.ceil(totalCount / pagination.pageSize)
      : undefined
  let resolvedPageCount = explicitPageCount ?? countedPageCount
  if (totalCountCapped && explicitPageCount === undefined) {
    // The count stopped at a cap, so the server's page tells whether another
    // follows: a full page has a next one, a short page is the last. A
    // stand-in shown while the page loads tells nothing.
    const isLastPage =
      (!isPlaceholderData && data.length < pagination.pageSize) ||
      (cappedLastPage !== null && isSamePosition(cappedLastPage, pagination))
    resolvedPageCount = isLastPage
      ? pagination.pageIndex + 1
      : Math.max(countedPageCount ?? 0, pagination.pageIndex + 2)
  }
  const resolvedEnableSorting =
    options.enableSorting ??
    (!manualPagination ||
      Boolean(options.sorting) ||
      Boolean(options.onSortingChange))

  const table = useReactTable({
    data,
    columns,
    rowCount: totalCount,
    pageCount: resolvedPageCount,
    meta: { totalCountCapped },
    state: {
      sorting,
      columnVisibility,
      columnSizing,
      rowSelection,
      expanded,
      columnFilters: options.columnFilters,
      globalFilter: options.globalFilter,
      pagination,
    },
    enableRowSelection: options.enableRowSelection,
    enableSorting: resolvedEnableSorting,
    getRowId: options.getRowId,
    getSubRows: options.getSubRows,
    // Only forward an explicit value: an undefined key would override
    // TanStack's default (true) for every table.
    ...(options.paginateExpandedRows === undefined
      ? {}
      : { paginateExpandedRows: options.paginateExpandedRows }),
    globalFilterFn: options.globalFilterFn,
    autoResetPageIndex: options.autoResetPageIndex,
    manualFiltering,
    manualPagination,
    manualSorting,
    enableColumnResizing: options.enableColumnResizing,
    columnResizeMode: 'onChange',
    onSortingChange,
    onColumnVisibilityChange,
    onColumnSizingChange,
    onRowSelectionChange,
    onExpandedChange,
    onColumnFiltersChange: options.onColumnFiltersChange,
    onGlobalFilterChange: options.onGlobalFilterChange,
    onPaginationChange,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: withFilteredRowModel
      ? getFilteredRowModel()
      : undefined,
    getPaginationRowModel: withPaginationRowModel
      ? getPaginationRowModel()
      : undefined,
    getSortedRowModel: withSortedRowModel ? getSortedRowModel() : undefined,
    getFacetedRowModel: withFacetedRowModel ? getFacetedRowModel() : undefined,
    getFacetedUniqueValues: withFacetedRowModel
      ? getFacetedUniqueValues()
      : undefined,
    getExpandedRowModel: withExpandedRowModel
      ? getExpandedRowModel()
      : undefined,
  })

  const actualPageCount = table.getPageCount()
  React.useEffect(() => {
    // A capped count does not know the last page; see the effect below.
    if (totalCountCapped) return
    ensurePageInRange?.(actualPageCount)
  }, [actualPageCount, ensurePageInRange, totalCountCapped])

  React.useEffect(() => {
    if (!totalCountCapped) {
      servedPageRef.current = undefined
      if (cappedLastPage !== null) setCappedLastPage(null)
      return
    }
    if (isPlaceholderData) return
    const served: ServedPage = {
      pageIndex: pagination.pageIndex,
      pageSize: pagination.pageSize,
      rows: data.length,
    }
    const previous = servedPageRef.current
    if (
      previous !== undefined &&
      isSamePosition(previous, served) &&
      previous.rows === served.rows
    ) {
      return
    }
    servedPageRef.current = served
    if (cappedLastPage !== null && !isSamePosition(cappedLastPage, served)) {
      setCappedLastPage(null)
    }
    if (served.rows > 0 || served.pageIndex === 0) return

    // An empty page past the first: the list has at most pageIndex pages.
    if (
      previous !== undefined &&
      previous.pageSize === served.pageSize &&
      previous.rows === served.pageSize &&
      previous.pageIndex < served.pageIndex
    ) {
      // Paged on from a full page past the end of the matches: show that
      // page again. Right before the empty page, it is the last page.
      if (previous.pageIndex === served.pageIndex - 1) {
        setCappedLastPage({
          pageIndex: previous.pageIndex,
          pageSize: previous.pageSize,
        })
      }
      ensurePageInRange?.(previous.pageIndex + 1, { resetTo: 'last' })
      return
    }
    ensurePageInRange?.(served.pageIndex)
  }, [
    cappedLastPage,
    data.length,
    ensurePageInRange,
    isPlaceholderData,
    pagination.pageIndex,
    pagination.pageSize,
    totalCountCapped,
  ])

  React.useEffect(() => {
    if (
      options.columnVisibility !== undefined ||
      columnVisibilityStorageKey ===
        hydratedColumnVisibilityStorageKeyRef.current
    ) {
      return
    }

    hydratedColumnVisibilityStorageKeyRef.current = columnVisibilityStorageKey
    skipNextColumnVisibilityPersistRef.current = true
    onColumnVisibilityChange(() => resolvedInitialColumnVisibility)
  }, [
    columnVisibilityStorageKey,
    onColumnVisibilityChange,
    options.columnVisibility,
    resolvedInitialColumnVisibility,
  ])

  React.useEffect(() => {
    if (
      options.columnSizing !== undefined ||
      columnSizingStorageKey === hydratedColumnSizingStorageKeyRef.current
    ) {
      return
    }

    hydratedColumnSizingStorageKeyRef.current = columnSizingStorageKey
    skipNextColumnSizingPersistRef.current = true
    onColumnSizingChange(() => resolvedInitialColumnSizing)
  }, [
    columnSizingStorageKey,
    onColumnSizingChange,
    options.columnSizing,
    resolvedInitialColumnSizing,
  ])

  React.useEffect(() => {
    if (!columnVisibilityStorageKey || typeof window === 'undefined') return

    if (skipNextColumnVisibilityPersistRef.current) {
      skipNextColumnVisibilityPersistRef.current = false
      return
    }

    try {
      window.localStorage.setItem(
        columnVisibilityStorageKey,
        JSON.stringify(columnVisibility)
      )
    } catch {
      // Storage can be unavailable in private mode; table controls still work.
    }
  }, [columnVisibility, columnVisibilityStorageKey])

  React.useEffect(() => {
    if (!columnSizingStorageKey || typeof window === 'undefined') return

    if (skipNextColumnSizingPersistRef.current) {
      skipNextColumnSizingPersistRef.current = false
      return
    }

    if (columnSizingPersistTimerRef.current !== undefined) {
      window.clearTimeout(columnSizingPersistTimerRef.current)
    }

    columnSizingPersistTimerRef.current = window.setTimeout(() => {
      try {
        window.localStorage.setItem(
          columnSizingStorageKey,
          JSON.stringify(columnSizing)
        )
      } catch {
        // Storage can be unavailable in private mode; table controls still work.
      } finally {
        columnSizingPersistTimerRef.current = undefined
      }
    }, COLUMN_SIZING_PERSIST_DELAY_MS)

    return () => {
      if (columnSizingPersistTimerRef.current !== undefined) {
        window.clearTimeout(columnSizingPersistTimerRef.current)
        columnSizingPersistTimerRef.current = undefined
      }
    }
  }, [columnSizing, columnSizingStorageKey])

  return {
    table,
  }
}
