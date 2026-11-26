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
/**
 * Which lazily loaded chunks belong to the console (used by rsbuild.config.ts
 * for its `console-*` split chunk groups).
 *
 * The console pages share most of their code (the UI kit, the data tables).
 * Below the size thresholds of the default split chunk groups, that code is
 * copied into every page chunk, so each console navigation downloads it
 * again. The console groups put it into shared chunks instead, but only for
 * console chunks: the public pages (home, sign-in, model square) keep their
 * own, smaller copies and never load the console's shared chunks.
 *
 * A chunk is a console chunk when every chunk group it belongs to is a
 * console group: the code-split part of a route file under
 * `src/routes/_authenticated/` (TanStack Router imports it with a
 * `?tsr-split=` query), or a group only ever loaded from console groups (a
 * dialog, an editor or a chart chunk of a console page). Chunks of the first
 * load are never console chunks.
 */

const ROUTE_SPLIT = /[\\/]src[\\/]routes[\\/].*\?tsr-split=/
const CONSOLE_ROUTE_SPLIT =
  /[\\/]src[\\/]routes[\\/]_authenticated[\\/].*\?tsr-split=/

// Chunk groups are long-lived objects of one compilation: remember answers.
const consoleGroups = new WeakMap()

// Deeper nesting of lazy imports than any page has; also ends a cycle.
const MAX_DEPTH = 32

/**
 * @param {{ origins?: Array<{ request?: string }>, getParents(): unknown[] }} group
 * @param {number} depth
 * @returns {boolean}
 */
function isConsoleGroup(group, depth) {
  const known = consoleGroups.get(group)
  if (known !== undefined) return known
  if (depth > MAX_DEPTH) return false

  const requests = (group.origins ?? []).map((origin) => origin.request ?? '')
  let result
  if (requests.some((request) => ROUTE_SPLIT.test(request))) {
    result = requests.every((request) => CONSOLE_ROUTE_SPLIT.test(request))
  } else {
    const parents = group.getParents()
    result =
      parents.length > 0 &&
      parents.every((parent) => isConsoleGroup(parent, depth + 1))
  }
  consoleGroups.set(group, result)
  return result
}

/**
 * Whether a chunk is only ever loaded by console pages.
 *
 * @param {{ canBeInitial(): boolean, groupsIterable: Iterable<unknown> }} chunk
 * @returns {boolean}
 */
export function isConsoleChunk(chunk) {
  if (chunk.canBeInitial()) return false
  const groups = [...chunk.groupsIterable]
  return groups.length > 0 && groups.every((group) => isConsoleGroup(group, 0))
}
