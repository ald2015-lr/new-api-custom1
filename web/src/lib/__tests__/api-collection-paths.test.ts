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
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

import { describe, expect, test } from 'vitest'

// The server registers these collections as "<group>/" only. A request to the
// bare path is answered with a 301 (GET) or 307 (POST/PUT, body sent twice)
// to the slash form: one extra round trip per list load or filter change.
// /api/task is registered without the slash and is deliberately not listed.
const SLASH_COLLECTIONS = [
  'channel',
  'log',
  'data',
  'group',
  'prefill_group',
  'mj',
  'token',
  'redemption',
  'vendors',
  'deployments',
  'option',
  'custom-oauth-provider',
  'models',
  'user',
]

const BARE_COLLECTION_PATH = new RegExp(
  String.raw`['"\x60]/api/(${SLASH_COLLECTIONS.join('|')})(?=['"\x60?]|\$\{)`
)

// Their items are registered as "<group>/:id", without the slash, so a
// request to "<group>/<id>/" costs the same extra redirect.
const ITEM_PATH_WITH_SLASH = new RegExp(
  String.raw`['"\x60]/api/(${SLASH_COLLECTIONS.join('|')})/\$\{[^}]+\}/(?=['"\x60?])`
)

const SRC = resolve(import.meta.dirname, '../..')

function sourceFiles(dir: string, files: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== '__tests__') sourceFiles(path, files)
    } else if (
      /\.tsx?$/.test(entry.name) &&
      !/\.(test|spec)\.tsx?$/.test(entry.name) &&
      entry.name !== 'routeTree.gen.ts'
    ) {
      files.push(path)
    }
  }
  return files
}

describe('collection endpoints', () => {
  test('the guard recognises the paths it is meant to catch', () => {
    const offending = [
      `api.get('/api/channel', { params })`,
      'api.get(`/api/log?${query}`)',
      `api.put("/api/prefill_group", data)`,
      'api.get(`/api/data${suffix}`)',
    ]
    const allowed = [
      `api.get('/api/channel/', { params })`,
      'api.get(`/api/log/self?${query}`)',
      `api.get('/api/task?p=1')`,
      `api.get('/api/channel/search')`,
      'api.get(`/api/channel/${id}`)',
    ]
    for (const line of offending) expect(line).toMatch(BARE_COLLECTION_PATH)
    for (const line of allowed) expect(line).not.toMatch(BARE_COLLECTION_PATH)
  })

  test('the guard recognises item paths that end in a slash', () => {
    const offending = [
      'api.delete(`/api/token/${id}/`)',
      '`/api/user/${id}/`,',
      'api.delete(`/api/redemption/${id}/?force=1`)',
    ]
    const allowed = [
      'api.delete(`/api/token/${id}`)',
      'api.post(`/api/token/${id}/key`)',
      '`/api/user/${userId}/oauth/bindings/${providerId}`',
      'api.get(`/api/channel/${id}`)',
      `api.get('/api/channel/', { params })`,
    ]
    for (const line of offending) expect(line).toMatch(ITEM_PATH_WITH_SLASH)
    for (const line of allowed) expect(line).not.toMatch(ITEM_PATH_WITH_SLASH)
  })

  test('are called as the server registers them everywhere in the app', () => {
    const violations: string[] = []
    for (const file of sourceFiles(SRC)) {
      const lines = readFileSync(file, 'utf8').split('\n')
      lines.forEach((line, index) => {
        if (
          BARE_COLLECTION_PATH.test(line) ||
          ITEM_PATH_WITH_SLASH.test(line)
        ) {
          violations.push(`${relative(SRC, file)}:${index + 1}: ${line.trim()}`)
        }
      })
    }
    expect(violations).toEqual([])
  })
})
