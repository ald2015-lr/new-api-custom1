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
 * Build-time slimming of the English locale (used by rsbuild.config.ts for
 * production builds only).
 *
 * Almost every entry of `src/i18n/locales/en.json` is an identity entry: the
 * key is its own English text. i18next answers a missing key with the key
 * itself and interpolates it exactly like a found value, and every other
 * locale falls back to `en`, so the identity entries change no rendered text.
 * They are still most of the entry chunk every visitor downloads, whatever
 * their language. The production bundle therefore gets en.json without them.
 *
 * Only exact identity entries are dropped (`value === key`): an entry whose
 * value differs from its key in any way (case, whitespace, punctuation) or is
 * not a string is kept unchanged. The source file, `scripts/sync-i18n.mjs`,
 * tests and development builds keep using the full file.
 *
 * `src/i18n/__tests__/slim-en.test.ts` checks that both versions translate
 * every key the same way and that the source code uses no i18next feature
 * that would read the dropped entries (default values, `<Trans>` children,
 * fallback key arrays, dynamic contexts).
 *
 * @param {string} source the JSON text of a locale file ({ "translation": {...} })
 * @returns {string} the JSON text without identity entries
 */
export function slimTranslationSource(source) {
  const locale = JSON.parse(source)
  const translation = locale?.translation
  if (
    translation === null ||
    typeof translation !== 'object' ||
    Array.isArray(translation)
  ) {
    throw new Error(
      'slimTranslationSource: expected a locale file shaped like { "translation": { ... } }'
    )
  }

  const kept = Object.fromEntries(
    Object.entries(translation).filter(
      ([key, value]) => typeof value !== 'string' || value !== key
    )
  )
  return JSON.stringify({ ...locale, translation: kept })
}
