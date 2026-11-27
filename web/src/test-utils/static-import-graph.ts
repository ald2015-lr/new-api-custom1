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
import { existsSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'

/**
 * The static import graph of the web sources, as the bundler puts it into one
 * chunk: `import` and `export … from` statements, without type-only imports
 * and without dynamic `import()`, which starts a chunk of its own. Lets a test
 * check that a page does not pull a module in before it is used.
 *
 * The parser is a few regular expressions over the formatted sources. It may
 * count an import the compiler drops (a value import used only as a type), so
 * it errs on the side of a larger graph.
 */

const SRC = path.resolve(import.meta.dirname, '..')

const EXTENSIONS = [
  '',
  '.ts',
  '.tsx',
  '.js',
  '.mjs',
  '/index.ts',
  '/index.tsx',
  '/index.js',
]

const STATIC_IMPORT =
  /^(import|export)\s+(type\s+)?([\w\s{},*$]*?)\s*from\s*['"]([^'"]+)['"]/gm
const SIDE_EFFECT_IMPORT = /^import\s*['"]([^'"]+)['"]/gm

function isTypeOnlyClause(clause: string): boolean {
  const named = /^\{([\s\S]*)\}$/.exec(clause.trim())
  if (!named) return false
  const specifiers = named[1]
    .split(',')
    .map((specifier) => specifier.trim())
    .filter(Boolean)
  return (
    specifiers.length > 0 &&
    specifiers.every((specifier) => specifier.startsWith('type '))
  )
}

/** Source file path (absolute), or `package:<name>` for a bare specifier. */
function resolveSpecifier(fromFile: string, specifier: string): string {
  let base: string
  if (specifier.startsWith('@/')) {
    base = path.join(SRC, specifier.slice(2))
  } else if (specifier.startsWith('.')) {
    base = path.resolve(path.dirname(fromFile), specifier)
  } else {
    return `package:${specifier}`
  }
  for (const extension of EXTENSIONS) {
    const candidate = base + extension
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate
  }
  throw new Error(`Cannot resolve ${specifier} from ${fromFile}`)
}

function staticImports(file: string): string[] {
  if (!/\.(ts|tsx|js|mjs)$/.test(file)) return []
  const source = readFileSync(file, 'utf8')
  const imports: string[] = []
  for (const match of source.matchAll(STATIC_IMPORT)) {
    const [, , typeOnly, clause, specifier] = match
    if (typeOnly || isTypeOnlyClause(clause)) continue
    imports.push(resolveSpecifier(file, specifier))
  }
  for (const match of source.matchAll(SIDE_EFFECT_IMPORT)) {
    imports.push(resolveSpecifier(file, match[1]))
  }
  return imports
}

/** A module of the graph: a source path relative to `src`, or `package:<name>`. */
function displayName(module: string): string {
  return module.startsWith('package:') ? module : path.relative(SRC, module)
}

export type StaticImportClosure = {
  /** Every module reached, by display name. */
  modules: Set<string>
  /** The import chain from the entry to a module, or `null`. */
  chainTo: (module: string) => string[] | null
}

/**
 * Every module that loading `entry` (a path relative to `src`) loads with it.
 */
export function staticImportClosure(entry: string): StaticImportClosure {
  const start = resolveSpecifier(path.join(SRC, 'index.ts'), `@/${entry}`)
  const parents = new Map<string, string | null>([[start, null]])
  const queue = [start]
  while (queue.length > 0) {
    const file = queue.shift() as string
    if (file.startsWith('package:')) continue
    for (const imported of staticImports(file)) {
      if (parents.has(imported)) continue
      parents.set(imported, file)
      queue.push(imported)
    }
  }
  const byName = new Map(
    [...parents.keys()].map((module) => [displayName(module), module])
  )
  return {
    modules: new Set(byName.keys()),
    chainTo(name) {
      let module = byName.get(name)
      if (!module) return null
      const chain: string[] = []
      while (module) {
        chain.unshift(displayName(module))
        module = parents.get(module) ?? undefined
      }
      return chain
    },
  }
}

/**
 * The modules of a closure that match `pattern`, each with the chain that
 * pulls it in: an empty list when the page does not load any of them.
 */
export function findStaticImports(
  closure: StaticImportClosure,
  pattern: RegExp
): string[] {
  return [...closure.modules]
    .filter((module) => pattern.test(module))
    .map((module) => (closure.chainTo(module) ?? [module]).join(' -> '))
}
