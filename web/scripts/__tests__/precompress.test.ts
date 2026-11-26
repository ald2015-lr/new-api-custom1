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
import { spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { brotliDecompressSync } from 'node:zlib'

import { afterEach, beforeEach, describe, expect, test } from 'vitest'

import { precompressDirectory } from '../precompress.mjs'

const SCRIPT = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../precompress.mjs'
)

// A compressible text well above the 1 KB floor.
const SOURCE = Array.from(
  { length: 200 },
  (_, i) => `export const value${i} = 'chunk ${i % 7}'\n`
).join('')

let dist: string

function write(relativePath: string, content: string | Buffer) {
  const file = join(dist, relativePath)
  mkdirSync(dirname(file), { recursive: true })
  writeFileSync(file, content)
  return file
}

beforeEach(() => {
  dist = mkdtempSync(join(tmpdir(), 'precompress-'))
})

afterEach(() => {
  rmSync(dist, { recursive: true, force: true })
})

describe('precompressDirectory', () => {
  test('JS, CSS, SVG and JSON files above 1 KB get a brotli copy that decodes to the file', async () => {
    const files = [
      write('static/js/index.abc.js', SOURCE),
      write('static/js/async/123.def.js', SOURCE.repeat(2)),
      write('static/css/index.abc.css', `.a{color:red}\n`.repeat(200)),
      write(
        'static/media/logo.svg',
        `<svg><path d="M0 0h1"/></svg>\n`.repeat(80)
      ),
      write('static/js/async/locale-fr.json', JSON.stringify({ a: SOURCE })),
    ]

    await precompressDirectory(join(dist, 'static'))

    for (const file of files) {
      const encoded = readFileSync(`${file}.br`)
      expect(encoded.length).toBeLessThan(readFileSync(file).length)
      expect(brotliDecompressSync(encoded).equals(readFileSync(file))).toBe(
        true
      )
    }
  })

  test('small files, other file types and files brotli cannot shrink get no copy', async () => {
    const small = write('static/js/tiny.js', 'export {}\n'.repeat(100))
    const image = write('static/image/logo.png', SOURCE)
    const license = write('static/js/index.js.LICENSE.txt', SOURCE)
    const random = write('static/js/random.js', randomBytes(4096))

    const results = await precompressDirectory(join(dist, 'static'))

    for (const file of [small, image, license, random]) {
      expect(existsSync(`${file}.br`)).toBe(false)
    }
    expect(results.map((result) => result.written)).toEqual([false, false])
  })

  test('a stale brotli copy of a file that no longer qualifies is removed', async () => {
    const random = write('static/js/random.js', randomBytes(4096))
    writeFileSync(`${random}.br`, 'stale')

    await precompressDirectory(join(dist, 'static'))

    expect(existsSync(`${random}.br`)).toBe(false)
  })

  test('the same build gives byte-identical brotli files', async () => {
    const file = write('static/js/index.abc.js', SOURCE)
    await precompressDirectory(join(dist, 'static'), { concurrency: 1 })
    const first = readFileSync(`${file}.br`)

    await precompressDirectory(join(dist, 'static'), { concurrency: 4 })

    expect(readFileSync(`${file}.br`).equals(first)).toBe(true)
    expect(existsSync(`${file}.br.br`)).toBe(false)
  })
})

test('the build step precompresses dist/static of the given dist directory', () => {
  const file = write('static/js/index.abc.js', SOURCE)
  write('index.html', `<!doctype html>${SOURCE}`)

  const run = spawnSync(process.execPath, [SCRIPT, dist], { encoding: 'utf8' })

  expect(run.status).toBe(0)
  expect(run.stdout).toContain('precompress: 1 of 1 files')
  expect(existsSync(`${file}.br`)).toBe(true)
  expect(existsSync(join(dist, 'index.html.br'))).toBe(false)
})
