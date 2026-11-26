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
 * Precompress the production build with brotli (runs after `rsbuild build`,
 * see the `build` script in package.json).
 *
 * Every JS, CSS, SVG and JSON file above 1 KB under dist/static gets a
 * `<file>.br` next to it, compressed at the highest quality (11). The server
 * embeds the build and sends the `.br` file to browsers that accept brotli
 * (middleware/frontend_static.go); everyone else keeps getting gzip. A file
 * whose brotli encoding would not be smaller gets no `.br` file. The output
 * depends only on the input bytes, so the same build gives the same files.
 *
 * Only node:zlib is used, so the script runs with Bun (the Docker builder
 * stage) and with Node alike, without extra tools.
 *
 * Usage: bun scripts/precompress.mjs [distDir]   (default: dist)
 */
import { readdir, readFile, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import zlib from 'node:zlib'

const brotliCompress = promisify(zlib.brotliCompress)

export const PRECOMPRESS_EXTENSIONS = new Set(['.js', '.css', '.svg', '.json'])
export const PRECOMPRESS_MIN_BYTES = 1024

/** The files under `dir` that get a brotli copy, in a stable order. */
export async function listPrecompressFiles(dir) {
  const entries = await readdir(dir, { withFileTypes: true, recursive: true })
  const files = []
  for (const entry of entries) {
    if (!entry.isFile()) continue
    if (!PRECOMPRESS_EXTENSIONS.has(path.extname(entry.name))) continue
    files.push(path.join(entry.parentPath ?? entry.path, entry.name))
  }
  return files.sort()
}

/**
 * Write `<file>.br` when the file is above the size floor and its brotli
 * encoding is smaller; otherwise make sure no stale `.br` stays next to it.
 */
export async function precompressFile(file) {
  const input = await readFile(file)
  const target = `${file}.br`
  if (input.length <= PRECOMPRESS_MIN_BYTES) {
    await rm(target, { force: true })
    return { file, inputBytes: input.length, outputBytes: 0, written: false }
  }
  const output = await brotliCompress(input, {
    params: {
      [zlib.constants.BROTLI_PARAM_QUALITY]: 11,
      [zlib.constants.BROTLI_PARAM_SIZE_HINT]: input.length,
    },
  })
  if (output.length >= input.length) {
    await rm(target, { force: true })
    return {
      file,
      inputBytes: input.length,
      outputBytes: output.length,
      written: false,
    }
  }
  await writeFile(target, output)
  return {
    file,
    inputBytes: input.length,
    outputBytes: output.length,
    written: true,
  }
}

/** Precompress every eligible file under `dir`, a few files at a time. */
export async function precompressDirectory(dir, options = {}) {
  const files = await listPrecompressFiles(dir)
  const concurrency = Math.max(
    1,
    options.concurrency ?? os.availableParallelism?.() ?? os.cpus().length
  )
  const results = Array.from({ length: files.length })
  let next = 0
  async function work() {
    while (next < files.length) {
      const index = next
      next += 1
      results[index] = await precompressFile(files[index])
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(concurrency, files.length) }, work)
  )
  return results
}

async function main() {
  const distDir = path.resolve(process.argv[2] ?? 'dist')
  const staticDir = path.join(distDir, 'static')
  const started = Date.now()
  const results = await precompressDirectory(staticDir)
  const written = results.filter((result) => result.written)
  const inputBytes = written.reduce((sum, result) => sum + result.inputBytes, 0)
  const outputBytes = written.reduce(
    (sum, result) => sum + result.outputBytes,
    0
  )
  console.log(
    `precompress: ${written.length} of ${results.length} files in ` +
      `${path.relative(process.cwd(), staticDir) || staticDir} got a .br ` +
      `(${inputBytes} -> ${outputBytes} bytes) in ` +
      `${((Date.now() - started) / 1000).toFixed(1)} s`
  )
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : ''
if (invokedPath === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error('precompress failed:', error)
    process.exit(1)
  })
}
