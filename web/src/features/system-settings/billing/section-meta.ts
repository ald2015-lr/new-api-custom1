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
 * Ids and titles of the billing and payment settings sections, without the section
 * components. The route guards and the sidebar import this file instead of
 * section-registry.tsx, so the settings code stays in the settings chunks.
 * Keep it in step with section-registry.tsx (same ids, order and default;
 * checked by section-meta-parity.test.ts).
 */
const BILLING_SECTIONS = [
  { id: 'quota', titleKey: 'Quota Settings' },
  { id: 'currency', titleKey: 'Currency & Display' },
  { id: 'model-pricing', titleKey: 'Model Pricing' },
  { id: 'group-pricing', titleKey: 'Group Pricing' },
  { id: 'group-access', titleKey: 'Group Access' },
  { id: 'group-billing', titleKey: 'Group Billing' },
  { id: 'payment', titleKey: 'Payment Gateway' },
  { id: 'checkin', titleKey: 'Check-in Rewards' },
] as const

type BillingSectionId = (typeof BILLING_SECTIONS)[number]['id']

const billingMeta = createSectionMeta<BillingSectionId>({
  sections: BILLING_SECTIONS,
  defaultSection: 'quota',
  basePath: '/system-settings/billing',
  urlStyle: 'path',
})

export const BILLING_SECTION_IDS = billingMeta.sectionIds
export const BILLING_DEFAULT_SECTION = billingMeta.defaultSection
export const getBillingSectionNavItems = billingMeta.getSectionNavItems
