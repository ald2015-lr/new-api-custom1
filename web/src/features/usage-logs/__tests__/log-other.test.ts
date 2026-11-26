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
import { describe, expect, test, vi } from 'vitest'

import { getLogOther } from '../lib/format'

describe('getLogOther', () => {
  test('repeated reads of one row return the same parsed object', () => {
    const row = { other: JSON.stringify({ frt: 812, group: 'vip' }) }

    const first = getLogOther(row)

    expect(first).toEqual({ frt: 812, group: 'vip' })
    expect(getLogOther(row)).toBe(first)
  })

  test('a row whose other text changes is parsed again', () => {
    const row = { other: JSON.stringify({ group: 'default' }) }
    getLogOther(row)

    row.other = JSON.stringify({ group: 'vip' })

    expect(getLogOther(row)).toEqual({ group: 'vip' })
  })

  test('two rows with the same other text get their own equal values', () => {
    const text = JSON.stringify({ billing_source: 'subscription' })

    const a = getLogOther({ other: text })
    const b = getLogOther({ other: text })

    expect(a).toEqual(b)
  })

  test('an empty or missing other field reads as no data', () => {
    expect(getLogOther({ other: '' })).toBeNull()
    expect(getLogOther({})).toBeNull()
  })

  test('invalid JSON reads as no data and is reported once per row', () => {
    const consoleError = vi
      .spyOn(console, 'error')
      .mockImplementation(() => undefined)
    const row = { other: '{not json' }

    expect(getLogOther(row)).toBeNull()
    expect(getLogOther(row)).toBeNull()
    expect(consoleError).toHaveBeenCalledTimes(1)
  })
})
