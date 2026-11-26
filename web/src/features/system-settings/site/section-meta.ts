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
 * Ids and titles of the site and branding settings sections, without the section
 * components. The route guards and the sidebar import this file instead of
 * section-registry.tsx, so the settings code stays in the settings chunks.
 * Keep it in step with section-registry.tsx (same ids, order and default;
 * checked by section-meta-parity.test.ts).
 */
const SITE_SECTIONS = [
  { id: 'system-info', titleKey: 'System Information' },
  { id: 'notice', titleKey: 'System Notice' },
  { id: 'header-navigation', titleKey: 'Header navigation' },
  { id: 'sidebar-modules', titleKey: 'Sidebar modules' },
] as const

type SiteSectionId = (typeof SITE_SECTIONS)[number]['id']

const siteMeta = createSectionMeta<SiteSectionId>({
  sections: SITE_SECTIONS,
  defaultSection: 'system-info',
  basePath: '/system-settings/site',
  urlStyle: 'path',
})

export const SITE_SECTION_IDS = siteMeta.sectionIds
export const SITE_DEFAULT_SECTION = siteMeta.defaultSection
export const getSiteSectionNavItems = siteMeta.getSectionNavItems
