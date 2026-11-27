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
import { createSectionMeta } from '../utils/section-meta'

/**
 * Ids and titles of the console content settings sections, without the section
 * components. The route guards and the sidebar import this file instead of
 * section-registry.tsx, so the settings code stays in the settings chunks.
 * Keep it in step with section-registry.tsx (same ids, order and default;
 * checked by ../__tests__/section-meta.test.tsx).
 */
const CONTENT_SECTIONS = [
  { id: 'dashboard', titleKey: 'Data Dashboard' },
  { id: 'announcements', titleKey: 'Announcements' },
  { id: 'api-info', titleKey: 'API Addresses' },
  { id: 'faq', titleKey: 'FAQ' },
  { id: 'uptime-kuma', titleKey: 'Uptime Kuma' },
  { id: 'chat', titleKey: 'Chat Presets' },
  { id: 'drawing', titleKey: 'Drawing' },
] as const

type ContentSectionId = (typeof CONTENT_SECTIONS)[number]['id']

const contentMeta = createSectionMeta<ContentSectionId>({
  sections: CONTENT_SECTIONS,
  defaultSection: 'dashboard',
  basePath: '/system-settings/content',
  urlStyle: 'path',
})

export const CONTENT_SECTION_IDS = contentMeta.sectionIds
export const CONTENT_DEFAULT_SECTION = contentMeta.defaultSection
export const getContentSectionNavItems = contentMeta.getSectionNavItems
