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
 * Ids and titles of the request policy settings sections, without the section
 * components. The route guards and the sidebar import this file instead of
 * section-registry.tsx, so the settings code stays in the settings chunks.
 * Keep it in step with section-registry.tsx (same ids, order and default;
 * checked by section-meta-parity.test.ts).
 */
const POLICY_SECTIONS = [
  { id: 'routing', titleKey: 'Sessions and retries' },
  { id: 'health', titleKey: 'Channel health' },
] as const

type PolicySectionId = (typeof POLICY_SECTIONS)[number]['id']

const policyMeta = createSectionMeta<PolicySectionId>({
  sections: POLICY_SECTIONS,
  defaultSection: 'routing',
  basePath: '/system-settings/request-policies',
  urlStyle: 'path',
})

export const POLICY_SECTION_IDS = policyMeta.sectionIds
export const POLICY_DEFAULT_SECTION = policyMeta.defaultSection
export const getPolicySectionNavItems = policyMeta.getSectionNavItems
