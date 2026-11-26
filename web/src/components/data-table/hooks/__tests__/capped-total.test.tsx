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
  ColumnDef,
  PaginationState,
  TableOptions,
} from '@tanstack/react-table'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import * as React from 'react'
import { expect, it, vi } from 'vitest'

import { DataTablePagination } from '../../core/pagination'
import { useDataTable } from '../use-data-table'

type Row = { id: number }

const PAGE_SIZE = 100
const columns: ColumnDef<Row, unknown>[] = [{ accessorKey: 'id' }]
// "10,000+" in whatever digit grouping the test runtime formats with.
const CAPPED_TOTAL = /^10[,.\s\u00a0\u202f]?000\+$/

/**
 * A server-paginated table over `matches` rows whose count stops at `cap`,
 * like the usage-log list: each page holds the rows the server would return,
 * or placeholderRows stand in for it while it loads.
 */
function ServerTable(props: {
  matches: number
  cap: number
  initialPage: number
  compact?: boolean
  placeholderRows?: number
  ensurePageInRange?: (pageCount: number) => void
}) {
  const [pagination, setPagination] = React.useState<PaginationState>({
    pageIndex: props.initialPage - 1,
    pageSize: PAGE_SIZE,
  })
  const pageRows =
    props.placeholderRows ??
    Math.max(
      0,
      Math.min(PAGE_SIZE, props.matches - pagination.pageIndex * PAGE_SIZE)
    )
  const data = React.useMemo(
    () =>
      Array.from({ length: pageRows }, (_, index) => ({
        id: pagination.pageIndex * PAGE_SIZE + index,
      })),
    [pageRows, pagination.pageIndex]
  )
  const { table } = useDataTable({
    data,
    columns,
    manualPagination: true,
    totalCount: Math.min(props.matches, props.cap),
    totalCountCapped: props.matches > props.cap,
    isPlaceholderData: props.placeholderRows !== undefined,
    pagination,
    onPaginationChange: setPagination,
    ensurePageInRange: props.ensurePageInRange,
  })
  return <DataTablePagination table={table} compact={props.compact} />
}

it('pages an exact total as before and clamps a page past its end', async () => {
  const ensurePageInRange = vi.fn()
  const user = userEvent.setup()
  const view = render(
    <ServerTable
      matches={250}
      cap={10000}
      initialPage={1}
      ensurePageInRange={ensurePageInRange}
    />
  )
  expect(screen.getByText('250')).toBeVisible()
  expect(screen.queryByText(/\+$/)).not.toBeInTheDocument()
  expect(ensurePageInRange).toHaveBeenLastCalledWith(3)

  await user.click(screen.getByRole('button', { name: 'Go to last page' }))
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled()

  view.unmount()
  ensurePageInRange.mockClear()
  render(
    <ServerTable
      matches={250}
      cap={10000}
      initialPage={5}
      ensurePageInRange={ensurePageInRange}
    />
  )
  expect(ensurePageInRange).toHaveBeenCalledWith(3)
})

it('shows a capped total with a plus and pages past it until a short page', async () => {
  const ensurePageInRange = vi.fn()
  const user = userEvent.setup()
  render(
    <ServerTable
      matches={10050}
      cap={10000}
      initialPage={100}
      ensurePageInRange={ensurePageInRange}
    />
  )
  expect(screen.getByText(CAPPED_TOTAL)).toBeVisible()
  const next = screen.getByRole('button', { name: 'Go to next page' })
  expect(next).toBeEnabled()

  await user.click(next)
  expect(screen.getByText('101', { selector: 'button' })).toBeVisible()
  expect(
    screen.queryByText('102', { selector: 'button' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled()
  expect(screen.getByText(CAPPED_TOTAL)).toBeVisible()
  expect(ensurePageInRange).not.toHaveBeenCalled()
})

it('keeps a page of a capped total that lies past the counted pages', () => {
  const ensurePageInRange = vi.fn()
  render(
    <ServerTable
      matches={20000}
      cap={10000}
      initialPage={150}
      ensurePageInRange={ensurePageInRange}
    />
  )
  expect(screen.getByText('151', { selector: 'button' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeEnabled()
  expect(ensurePageInRange).not.toHaveBeenCalled()
})

it('keeps the next page of a capped total while a short stand-in page is shown', () => {
  render(
    <ServerTable
      matches={20000}
      cap={10000}
      initialPage={150}
      placeholderRows={7}
    />
  )
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeEnabled()
  expect(screen.getByText('151', { selector: 'button' })).toBeVisible()
})

it('disables going to the last page while a capped total has more pages', () => {
  render(<ServerTable matches={20000} cap={10000} initialPage={1} />)
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeEnabled()
  expect(screen.getByRole('button', { name: 'Go to last page' })).toBeDisabled()
})

it('shows the page count of a capped total as a lower bound in compact mode', async () => {
  const user = userEvent.setup()
  render(<ServerTable matches={10050} cap={10000} initialPage={100} compact />)
  expect(screen.getByText(CAPPED_TOTAL)).toBeVisible()
  expect(screen.getByText('100 / 101+')).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  expect(screen.getByText('101 / 101')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled()
})

it('types the capped flag of the table meta as a boolean', () => {
  type Meta = NonNullable<TableOptions<Row>['meta']>
  // bun check covers the test files: if the TableMeta augmentation in
  // tanstack-table.d.ts stops merging, the meta is untyped, the string below
  // type-checks and the unused directive fails the check.
  // @ts-expect-error totalCountCapped is a boolean, not a string
  const wrong: Meta = { totalCountCapped: 'yes' }
  const right: Meta = { totalCountCapped: true }
  expect([wrong, right]).toHaveLength(2)
})
