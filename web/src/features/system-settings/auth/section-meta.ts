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
 * Ids and titles of the authentication settings sections, without the section
 * components. The route guards and the sidebar import this file instead of
 * section-registry.tsx, so the settings code stays in the settings chunks.
 * Keep it in step with section-registry.tsx (same ids, order and default;
 * checked by section-meta-parity.test.ts).
 */
const AUTH_SECTIONS = [
  { id: 'basic-auth', titleKey: 'Basic Authentication' },
  { id: 'oauth', titleKey: 'OAuth Integrations' },
  { id: 'passkey', titleKey: 'Passkey Authentication' },
  { id: 'bot-protection', titleKey: 'Bot Protection' },
  { id: 'invite-abuse', titleKey: 'Invite abuse protection' },
  { id: 'custom-oauth', titleKey: 'Custom OAuth' },
] as const

type AuthSectionId = (typeof AUTH_SECTIONS)[number]['id']

const authMeta = createSectionMeta<AuthSectionId>({
  sections: AUTH_SECTIONS,
  defaultSection: 'basic-auth',
  basePath: '/system-settings/auth',
  urlStyle: 'path',
})

export const AUTH_SECTION_IDS = authMeta.sectionIds
export const AUTH_DEFAULT_SECTION = authMeta.defaultSection
export const getAuthSectionNavItems = authMeta.getSectionNavItems
