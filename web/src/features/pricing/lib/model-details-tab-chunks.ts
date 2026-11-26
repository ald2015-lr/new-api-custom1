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

// The performance charts (the chart library alone is about 560 KB) and the
// highlighted code samples (an editor of about 175 KB) are chunks of their
// own: they load when the Performance or API tab of the model details opens
// (see components/model-details-lazy.tsx), or earlier through
// prefetchModelDetailsTab, not with the details.
export const loadModelDetailsCharts = () =>
  import('../components/model-details-charts')
export const loadModelDetailsCodeSample = () =>
  import('../components/model-details-code-sample')

/**
 * Start downloading what a details tab shows, for example when the pointer
 * reaches the tab. Other tabs load nothing; failures are left to the tab,
 * which loads the chunk again when it renders.
 */
export function prefetchModelDetailsTab(tab: string): void {
  if (tab === 'performance') loadModelDetailsCharts().catch(() => undefined)
  if (tab === 'api') loadModelDetailsCodeSample().catch(() => undefined)
}
