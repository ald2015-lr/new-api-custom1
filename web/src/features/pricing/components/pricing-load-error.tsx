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
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { getServerErrorMessage } from '@/lib/server-error-message'

/**
 * Shown instead of an empty model list when the model pricing could not be
 * loaded, so a failure never reads as "0 models".
 */
export function PricingLoadError(props: {
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation()
  return (
    <ErrorState
      className='rounded-lg border border-dashed'
      title={t('Failed to load model pricing')}
      description={getServerErrorMessage(props.error)}
      onRetry={props.onRetry}
    />
  )
}
