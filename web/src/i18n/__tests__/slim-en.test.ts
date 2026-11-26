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
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import i18next, { type i18n as I18nInstance } from 'i18next'
import { describe, expect, test } from 'vitest'

import { slimTranslationSource } from '../../../scripts/slim-locale.mjs'

// Production builds ship en.json through slimTranslationSource (see
// rsbuild.config.ts). These tests keep that invisible: the slim file must
// translate every key exactly like the full one, and the code must not use an
// i18next feature that reads the dropped identity entries.

type Translation = Record<string, unknown>

const I18N_DIR = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '..'
)
const SRC_DIR = path.resolve(I18N_DIR, '..')
const LOCALE_FILES: Record<string, string> = {
  zhCN: 'zh.json',
  zhTW: 'zh-TW.json',
  fr: 'fr.json',
  ru: 'ru.json',
  ja: 'ja.json',
  vi: 'vi.json',
}
// The options of src/i18n/config.ts that decide how a key resolves (the
// detector and react bindings do not take part).
const APP_INIT_OPTIONS = {
  partialBundledLanguages: true,
  fallbackLng: 'en',
  supportedLngs: ['en', 'zhCN', 'fr', 'ru', 'ja', 'vi', 'zhTW'],
  load: 'currentOnly',
  nsSeparator: false,
  interpolation: { escapeValue: false },
} as const
const CONFIG_OPTION_NAMES = [
  'resources',
  'partialBundledLanguages',
  'fallbackLng',
  'supportedLngs',
  'load',
  'nsSeparator',
  'debug',
  'interpolation',
  'detection',
]

function readLocaleSource(file: string): string {
  return readFileSync(path.join(I18N_DIR, 'locales', file), 'utf8')
}

function translationOf(source: string): Translation {
  return (JSON.parse(source) as { translation: Translation }).translation
}

const fullEnSource = readLocaleSource('en.json')
const fullEn = translationOf(fullEnSource)
const slimEn = translationOf(slimTranslationSource(fullEnSource))

async function createI18n(
  en: Translation,
  lng: string,
  locale?: Translation
): Promise<I18nInstance> {
  const instance = i18next.createInstance()
  const resources: Record<string, { translation: Translation }> = {
    en: { translation: en },
  }
  if (locale) resources[lng] = { translation: locale }
  await instance.init({ ...APP_INIT_OPTIONS, resources, lng })
  return instance
}

describe('slimTranslationSource', () => {
  test('the slim English file keeps every non-identity entry with its exact value and nothing else', () => {
    const expected = Object.entries(fullEn).filter(
      ([key, value]) => value !== key
    )

    expect(Object.entries(slimEn)).toEqual(expected)
    expect(expected.length).toBeGreaterThan(0)
    expect(Object.keys(slimEn).length).toBeLessThan(
      Object.keys(fullEn).length / 10
    )
  })

  test('entries that differ from their key only by case, whitespace or type are kept', () => {
    const source =
      '{"translation":{"Save":"Save","Save ":"Save","save":"Save",' +
      '"Count":3,"Nested":{"a":"b"},"__proto__":"x"}}'

    const slim = translationOf(slimTranslationSource(source))

    expect(Object.keys(slim)).toEqual([
      'Save ',
      'save',
      'Count',
      'Nested',
      '__proto__',
    ])
    expect(slim.Nested).toEqual({ a: 'b' })
    expect(Object.getOwnPropertyDescriptor(slim, '__proto__')?.value).toBe('x')
  })

  test('a file without a translation object fails the build', () => {
    expect(() => slimTranslationSource('{"Save":"Save"}')).toThrow(
      /translation/
    )
  })
})

describe('translations with the slim English file', () => {
  test('the parity check uses every option src/i18n/config.ts passes to init', () => {
    const config = readFileSync(path.join(I18N_DIR, 'config.ts'), 'utf8')
    const options = config.slice(
      config.indexOf('.init({'),
      config.indexOf('\n  })', config.indexOf('.init({'))
    )
    const names = [...options.matchAll(/^ {4}(\w+):/gm)].map((m) => m[1])

    expect(names).toEqual(CONFIG_OPTION_NAMES)
  })

  test.each(['en', ...Object.keys(LOCALE_FILES)])(
    'every key translates the same with the full and the slim English file in %s',
    async (lng) => {
      const locale =
        lng === 'en'
          ? undefined
          : translationOf(readLocaleSource(LOCALE_FILES[lng]))
      const full = await createI18n(fullEn, lng, locale)
      const slim = await createI18n(slimEn, lng, locale)
      const fileKeys = [...Object.keys(fullEn), ...Object.keys(locale ?? {})]
      // Plural forms resolve through their base key.
      const pluralBases = fileKeys.flatMap(
        (key) => /^(.+)_(zero|one|two|few|many|other)$/.exec(key)?.[1] ?? []
      )
      const keys = new Set([...fileKeys, ...pluralBases])
      const params = [
        undefined,
        { count: 1 },
        { count: 3, name: 'x', amount: 2, value: 5, model: 'm' },
      ]

      const differences: string[] = []
      for (const key of keys) {
        for (const options of params) {
          const a = full.t(key, options)
          const b = slim.t(key, options)
          if (a !== b) differences.push(`${key} ${JSON.stringify(options)}`)
        }
      }

      expect(differences).toEqual([])
    }
  )
})

// ---------------------------------------------------------------------------
// Source scan: i18next features that would read a dropped identity entry.
// ---------------------------------------------------------------------------

type Hazard = { file: string; reason: string; snippet: string }
type ContextUse = { key: string; context: string }

/** Index just past the string or template literal that starts at `start`. */
function skipQuoted(source: string, start: number): number {
  const quote = source[start]
  let i = start + 1
  while (i < source.length) {
    const char = source[i]
    if (char === '\\') {
      i += 2
      continue
    }
    if (quote === '`' && char === '$' && source[i + 1] === '{') {
      i = skipBalanced(source, i + 1)
      continue
    }
    if (char === quote) return i + 1
    i += 1
  }
  return i
}

/** Index just past the bracket group that opens at `start`. */
function skipBalanced(source: string, start: number): number {
  const closing: Record<string, string> = { '(': ')', '[': ']', '{': '}' }
  const stack = [closing[source[start]]]
  let i = start + 1
  while (i < source.length && stack.length > 0) {
    const char = source[i]
    if (char === "'" || char === '"' || char === '`') {
      i = skipQuoted(source, i)
      continue
    }
    if (char === '/' && source[i + 1] === '/') {
      i = source.indexOf('\n', i)
      if (i === -1) return source.length
      continue
    }
    if (char === '/' && source[i + 1] === '*') {
      i = source.indexOf('*/', i + 2) + 2
      if (i === 1) return source.length
      continue
    }
    if (closing[char]) stack.push(closing[char])
    else if (char === stack.at(-1)) stack.pop()
    i += 1
  }
  return i
}

/** The top-level arguments of the call whose `(` is at `open`. */
function callArguments(source: string, open: number): string[] {
  const end = skipBalanced(source, open) - 1
  const args: string[] = []
  let from = open + 1
  let i = from
  while (i < end) {
    const char = source[i]
    if (char === "'" || char === '"' || char === '`') {
      i = skipQuoted(source, i)
      continue
    }
    if (char === '(' || char === '[' || char === '{') {
      i = skipBalanced(source, i)
      continue
    }
    if (char === ',') {
      args.push(source.slice(from, i).trim())
      from = i + 1
    }
    i += 1
  }
  const last = source.slice(from, end).trim()
  if (last) args.push(last)
  return args
}

const STRING_LITERAL = /^(['"`])([\s\S]*)\1$/

function scanSource(file: string, source: string) {
  const hazards: Hazard[] = []
  const contexts: ContextUse[] = []
  const report = (reason: string, at: number) =>
    hazards.push({
      file,
      reason,
      snippet: source.slice(at, at + 80).replaceAll(/\s+/g, ' '),
    })

  for (const match of source.matchAll(/(?:^|[^\w$.])(t)\(|\.(t)\(/g)) {
    const open = match.index + match[0].length - 1
    const [key, ...rest] = callArguments(source, open)
    if (key === undefined) continue
    if (key.startsWith('[')) report('fallback key array', open)
    if (rest[0] && STRING_LITERAL.test(rest[0])) report('default value', open)
    const options = rest.join(',')
    if (/(?:^|[{,\s])defaultValue\s*[:,}]/.test(options)) {
      report('defaultValue option', open)
    }
    if (/(?:^|[{,\s])context\s*[:,}]/.test(options)) {
      const literal = /(?:^|[{,\s])context\s*:\s*(['"])([^'"]*)\1/.exec(options)
      const keyLiteral = STRING_LITERAL.exec(key)
      if (literal && keyLiteral && keyLiteral[1] !== '`') {
        contexts.push({ key: keyLiteral[2], context: literal[2] })
      } else {
        report('dynamic context', open)
      }
    }
  }

  for (const match of source.matchAll(/<Trans(?![\w$])/g)) {
    let i = match.index + match[0].length
    while (i < source.length && source[i] !== '>') {
      const char = source[i]
      if (char === '{') i = skipBalanced(source, i)
      else if (char === "'" || char === '"') i = skipQuoted(source, i)
      else i += 1
    }
    const selfClosing = source[i - 1] === '/'
    const emptyBody = /^>\s*<\/Trans>/.test(source.slice(i))
    if (!selfClosing && !emptyBody) report('<Trans> with children', match.index)
  }

  return { hazards, contexts }
}

function sourceFiles(dir: string): string[] {
  const files: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== '__tests__') files.push(...sourceFiles(full))
    } else if (
      /\.(ts|tsx)$/.test(entry.name) &&
      !/\.(test|spec)\.tsx?$/.test(entry.name) &&
      !entry.name.endsWith('.gen.ts')
    ) {
      files.push(full)
    }
  }
  return files
}

describe('source scan for i18next features that read identity entries', () => {
  test('the scan recognises default values, fallback keys, dynamic contexts and Trans children', () => {
    const fixture = [
      "t('Save', 'Store')",
      "i18n.t('Save', { defaultValue: 'Store' })",
      "t('Save', { count, defaultValue })",
      "t(['Save', 'Store'])",
      't(`Save`, { context: kind })',
      "<Trans i18nKey='Save'>Store <b>now</b></Trans>",
      "t('Tokens', { context: 'usage' })",
      "<Trans t={t} i18nKey='Pay {{amount}}' values={{ amount }} />",
      "t('Save {{name}}', { name: format(name, ', ') })",
    ].join('\n')

    const { hazards, contexts } = scanSource('fixture.tsx', fixture)

    expect(hazards.map((hazard) => hazard.reason)).toEqual([
      'default value',
      'defaultValue option',
      'defaultValue option',
      'fallback key array',
      'dynamic context',
      '<Trans> with children',
    ])
    expect(contexts).toEqual([{ key: 'Tokens', context: 'usage' }])
  })

  const scanned = sourceFiles(SRC_DIR).map((file) =>
    scanSource(path.relative(SRC_DIR, file), readFileSync(file, 'utf8'))
  )

  test('the app source uses none of them', () => {
    expect(scanned.flatMap((result) => result.hazards)).toEqual([])
  })

  test('every literal context translates the same with the slim English file', async () => {
    const contexts = scanned.flatMap((result) => result.contexts)
    expect(contexts.length).toBeGreaterThan(0)

    for (const lng of ['en', 'zhCN']) {
      const locale =
        lng === 'en'
          ? undefined
          : translationOf(readLocaleSource(LOCALE_FILES[lng]))
      const full = await createI18n(fullEn, lng, locale)
      const slim = await createI18n(slimEn, lng, locale)
      for (const use of contexts) {
        expect(slim.t(use.key, { context: use.context })).toBe(
          full.t(use.key, { context: use.context })
        )
      }
    }
  })
})
