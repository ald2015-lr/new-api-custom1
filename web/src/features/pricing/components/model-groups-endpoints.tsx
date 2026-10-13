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

import { cn } from '@/lib/utils'

/**
 * Compact "Groups default +1 · Endpoints openai, anthropic" summary of a model
 * card; renders nothing when both lists are empty.
 */
export function ModelGroupsEndpoints(props: {
  groups: readonly string[]
  endpoints: readonly string[]
}) {
  const { t } = useTranslation()
  const groups = props.groups
  const endpoints = props.endpoints
  if (groups.length === 0 && endpoints.length === 0) return null

  return (
    <dl
      className={cn(
        'grid min-w-0 grid-cols-2 gap-3 text-xs',
        (groups.length === 0 || endpoints.length === 0) && 'grid-cols-1'
      )}
    >
      {groups.length > 0 && (
        <div className='flex min-w-0 items-baseline gap-1.5'>
          <dt className='text-muted-foreground shrink-0'>{t('Groups')}</dt>
          <dd className='flex min-w-0 items-baseline gap-1'>
            <span className='truncate' title={groups.join(', ')}>
              {groups[0]}
            </span>
            {groups.length > 1 && (
              <span
                className='text-muted-foreground shrink-0'
                title={groups.slice(1).join(', ')}
              >
                +{groups.length - 1}
              </span>
            )}
          </dd>
        </div>
      )}
      {endpoints.length > 0 && (
        <div className='flex min-w-0 items-baseline gap-1.5'>
          <dt className='text-muted-foreground shrink-0'>{t('Endpoints')}</dt>
          <dd className='flex min-w-0 items-baseline gap-1'>
            <span className='truncate' title={endpoints.join(', ')}>
              {endpoints.slice(0, 2).join(', ')}
            </span>
            {endpoints.length > 2 && (
              <span
                className='text-muted-foreground shrink-0'
                title={endpoints.slice(2).join(', ')}
              >
                +{endpoints.length - 2}
              </span>
            )}
          </dd>
        </div>
      )}
    </dl>
  )
}
