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
 * Ids and titles of the operations settings sections, without the section
 * components. The route guards and the sidebar import this file instead of
 * section-registry.tsx, so the settings code stays in the settings chunks.
 * Keep it in step with section-registry.tsx (same ids, order and default;
 * checked by ../__tests__/section-meta.test.tsx).
 */
const OPERATIONS_SECTIONS = [
  { id: 'behavior', titleKey: 'System Behavior' },
  { id: 'alerts', titleKey: 'Model performance metrics' },
  { id: 'email', titleKey: 'Email & Notifications' },
  { id: 'worker', titleKey: 'Worker Proxy' },
  { id: 'logs', titleKey: 'Log Maintenance' },
  { id: 'performance', titleKey: 'Performance' },
  { id: 'update-checker', titleKey: 'System maintenance' },
] as const

type OperationsSectionId = (typeof OPERATIONS_SECTIONS)[number]['id']

const operationsMeta = createSectionMeta<OperationsSectionId>({
  sections: OPERATIONS_SECTIONS,
  defaultSection: 'behavior',
  basePath: '/system-settings/operations',
  urlStyle: 'path',
})

export const OPERATIONS_SECTION_IDS = operationsMeta.sectionIds
export const OPERATIONS_DEFAULT_SECTION = operationsMeta.defaultSection
export const getOperationsSectionNavItems = operationsMeta.getSectionNavItems
