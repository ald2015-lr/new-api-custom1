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
import { renderHook } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { type NavigateFn, useTableUrlState } from '../use-table-url-state'

type Search = Record<string, unknown>

/** The URL state of a table at search, navigating through a stub router. */
function renderUrlState(search: Search) {
  const navigate = vi.fn<NavigateFn>()
  const view = renderHook(
    (props: { search: Search }) =>
      useTableUrlState({
        search: props.search,
        navigate,
        pagination: { defaultPageSize: 10 },
      }),
    { initialProps: { search } }
  )
  return { ...view, navigate }
}

afterEach(() => {
  localStorage.clear()
})

it('keeps ensurePageInRange the same function across renders until the search changes', () => {
  const search = { page: 5, pageSize: 10 }
  const { result, rerender } = renderUrlState(search)
  const first = result.current.ensurePageInRange

  rerender({ search })
  expect(result.current.ensurePageInRange).toBe(first)

  rerender({ search: { pageSize: 10 } })
  expect(result.current.ensurePageInRange).not.toBe(first)
})

it('replaces a page past the page count with the first page and reports the move', () => {
  const { result, navigate } = renderUrlState({ page: 5, pageSize: 10 })

  expect(result.current.ensurePageInRange(3)).toBe(true)

  expect(navigate).toHaveBeenCalledOnce()
  const [options] = navigate.mock.calls[0]
  expect(options.replace).toBe(true)
  expect(
    typeof options.search === 'function' &&
      options.search({ page: 5, pageSize: 10 })
  ).toEqual({ page: undefined, pageSize: 10 })
})

it('leaves a page within the page count where it is and reports no move', () => {
  const { result, navigate } = renderUrlState({ page: 3, pageSize: 10 })

  expect(result.current.ensurePageInRange(3)).toBe(false)

  expect(navigate).not.toHaveBeenCalled()
})
