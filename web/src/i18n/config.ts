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
import i18n, { type ResourceLanguage } from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import { convertDetectedLanguage } from './languages'
import en from './locales/en.json'

type LocaleModule = { default: ResourceLanguage }

// Only `en` ships in the synchronous entry chunk. Every other locale is a
// lazily loaded async chunk keyed by the language codes i18next uses in this
// app (`supportedLngs` below / `INTERFACE_LANGUAGE_OPTIONS`), so a session only
// downloads the single bundle it renders.
const LOCALE_LOADERS = new Map<string, () => Promise<LocaleModule>>([
  [
    'zhCN',
    () => import(/* webpackChunkName: "locale-zhCN" */ './locales/zh.json'),
  ],
  [
    'zhTW',
    () => import(/* webpackChunkName: "locale-zhTW" */ './locales/zh-TW.json'),
  ],
  ['fr', () => import(/* webpackChunkName: "locale-fr" */ './locales/fr.json')],
  ['ru', () => import(/* webpackChunkName: "locale-ru" */ './locales/ru.json')],
  ['ja', () => import(/* webpackChunkName: "locale-ja" */ './locales/ja.json')],
  ['vi', () => import(/* webpackChunkName: "locale-vi" */ './locales/vi.json')],
])

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: { en },
    partialBundledLanguages: true,
    fallbackLng: 'en',
    supportedLngs: ['en', 'zhCN', 'fr', 'ru', 'ja', 'vi', 'zhTW'],
    load: 'currentOnly',
    nsSeparator: false, // Allow literal colons in keys (e.g., URLs, labels)
    debug: import.meta.env.DEV,
    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      // Browsers report `zh-CN`/`zh-TW`/`zh`; map them onto our `zhCN`/`zhTW`
      // codes (non-Chinese codes pass through for normal supportedLngs matching).
      convertDetectedLanguage,
    },
  })

/**
 * Register the translation bundle for `lng` before that language is activated
 * (call it ahead of `i18n.changeLanguage` and before the first render).
 *
 * Resolves immediately when the bundle is already present or when `lng` has no
 * lazy bundle (`en`, unknown codes). A failed chunk download is logged and
 * swallowed so the UI degrades to the fallback language instead of breaking.
 */
export async function ensureLocale(lng: string): Promise<void> {
  if (!lng || i18n.hasResourceBundle(lng, 'translation')) return
  const loader = LOCALE_LOADERS.get(lng)
  if (!loader) return
  try {
    const bundle = await loader()
    i18n.addResourceBundle(
      lng,
      'translation',
      bundle.default.translation,
      true,
      true
    )
  } catch (error) {
    // eslint-disable-next-line no-console
    console.error(
      `Failed to load the "${lng}" locale bundle; falling back to the default language.`,
      error
    )
  }
}

export default i18n
