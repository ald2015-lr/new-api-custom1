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
import i18n from 'i18next'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { ensureLocale } from '../config'

const LAZY_LANGUAGES = ['fr', 'ja', 'vi'] as const

describe('ensureLocale', () => {
  afterEach(() => {
    for (const lng of LAZY_LANGUAGES) {
      i18n.removeResourceBundle(lng, 'translation')
    }
    vi.doUnmock('../locales/vi.json')
  })

  test('loads a lazy locale on first request and registers its bundle exactly once', async () => {
    const addResourceBundle = vi.spyOn(i18n, 'addResourceBundle')

    await ensureLocale('fr')
    await ensureLocale('fr')

    expect(addResourceBundle).toHaveBeenCalledTimes(1)
    expect(addResourceBundle).toHaveBeenCalledWith(
      'fr',
      'translation',
      expect.any(Object),
      true,
      true
    )
    expect(i18n.hasResourceBundle('fr', 'translation')).toBe(true)
    expect(
      i18n.getResourceBundle('fr', 'translation')['Change language']
    ).toEqual(expect.any(String))
  })

  test('returns without loading when the bundle already exists', async () => {
    i18n.addResourceBundle(
      'ja',
      'translation',
      { Hello: 'こんにちは' },
      true,
      true
    )
    const addResourceBundle = vi.spyOn(i18n, 'addResourceBundle')

    await ensureLocale('ja')

    expect(addResourceBundle).not.toHaveBeenCalled()
    expect(i18n.getResourceBundle('ja', 'translation')).toEqual({
      Hello: 'こんにちは',
    })
  })

  test('resolves without registering a bundle when the locale chunk fails to load', async () => {
    vi.doMock('../locales/vi.json', () => {
      throw new Error('chunk download failed')
    })
    const consoleError = vi
      .spyOn(console, 'error')
      .mockImplementation(() => undefined)
    const addResourceBundle = vi.spyOn(i18n, 'addResourceBundle')

    await expect(ensureLocale('vi')).resolves.toBeUndefined()

    expect(addResourceBundle).not.toHaveBeenCalled()
    expect(i18n.hasResourceBundle('vi', 'translation')).toBe(false)
    expect(consoleError).toHaveBeenCalledTimes(1)
  })

  test('registering the bundle of the already active language updates resolvedLanguage', async () => {
    // Bootstrap path: the detector picked `fr` before its bundle existed, so
    // i18next resolved to the fallback. Loading the bundle must re-resolve,
    // otherwise number/date formatting keeps using the fallback locale.
    await i18n.changeLanguage('fr')
    expect(i18n.language).toBe('fr')
    expect(i18n.resolvedLanguage).not.toBe('fr')

    await ensureLocale('fr')

    expect(i18n.resolvedLanguage).toBe('fr')
    expect(i18n.t('Change language')).not.toBe('Change language')

    await i18n.changeLanguage('en')
  })

  test('is a no-op for a language without a lazy bundle', async () => {
    const addResourceBundle = vi.spyOn(i18n, 'addResourceBundle')

    await expect(ensureLocale('xx')).resolves.toBeUndefined()
    await expect(ensureLocale('')).resolves.toBeUndefined()

    expect(addResourceBundle).not.toHaveBeenCalled()
    expect(i18n.hasResourceBundle('xx', 'translation')).toBe(false)
  })
})
