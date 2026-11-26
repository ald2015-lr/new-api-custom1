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
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  getAllLogs,
  getAllMidjourneyLogs,
  getAllTaskLogs,
  getLogStats,
  getUserLogs,
  getUserLogStats,
  getUserMidjourneyLogs,
  getUserTaskLogs,
} from '../api'

function requestedPath(): string {
  const [url] = vi.mocked(api.get).mock.calls.at(-1) ?? ['']
  return String(url).split('?')[0]
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('usage log request paths', () => {
  test.each([
    ['admin logs', () => getAllLogs({}), '/api/log/'],
    ['own logs', () => getUserLogs({}), '/api/log/self'],
    ['admin drawing logs', () => getAllMidjourneyLogs({}), '/api/mj/'],
    ['own drawing logs', () => getUserMidjourneyLogs({}), '/api/mj/self'],
    // /api/task is registered without the trailing slash.
    ['admin task logs', () => getAllTaskLogs({}), '/api/task'],
    ['own task logs', () => getUserTaskLogs({}), '/api/task/self'],
  ])('a page of %s comes from %s', async (_name, request, path) => {
    vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true } })

    await request()

    expect(requestedPath()).toBe(path)
    expect(vi.mocked(api.get).mock.calls.at(-1)?.[0]).toMatch(/\?p=1&/)
  })

  test.each([
    ['admin log stats', () => getLogStats({}), '/api/log/stat'],
    ['own log stats', () => getUserLogStats({}), '/api/log/self/stat'],
  ])('%s come from %s', async (_name, request, path) => {
    vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true } })

    await request()

    expect(requestedPath()).toBe(path)
  })
})
