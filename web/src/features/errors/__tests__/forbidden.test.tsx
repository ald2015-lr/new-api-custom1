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
import { render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import { ForbiddenError } from '../forbidden'

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouter: () => ({ history: { go: vi.fn() } }),
}))

// The reason is one sentence, so it wraps where the language allows rather
// than at a fixed break between two separately translated halves.
it.each([
  ['zh', '您没有查看此资源所需的权限。'],
  ['en', "You don't have the necessary permission to view this resource."],
] as const)(
  'the 403 page gives its reason as one %s sentence',
  async (language, reason) => {
    const i18n = createInstance()
    await i18n.init({
      lng: language,
      fallbackLng: false,
      nsSeparator: false,
      resources: { en, zh },
      initAsync: false,
    })
    render(
      <I18nextProvider i18n={i18n}>
        <ForbiddenError />
      </I18nextProvider>
    )

    const paragraph = screen.getByText(reason)
    expect(paragraph.tagName).toBe('P')
    expect(paragraph.querySelector('br')).toBeNull()
  }
)
