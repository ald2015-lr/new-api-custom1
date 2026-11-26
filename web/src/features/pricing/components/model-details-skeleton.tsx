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
import { Skeleton } from '@/components/ui/skeleton'

const SKELETON_KEYS = ['first', 'second', 'third', 'fourth']

/**
 * Placeholder of the model details while their data or code loads; the page
 * version also holds the place of its back link.
 */
export function ModelDetailsSkeleton(props: { backLink?: boolean }) {
  return (
    <div aria-busy='true'>
      {props.backLink && <Skeleton className='mb-4 h-5 w-16' />}
      <div className='space-y-2'>
        <Skeleton className='h-7 w-64' />
        <Skeleton className='h-4 w-40' />
        <Skeleton className='h-4 w-full max-w-md' />
      </div>
      <div className='mt-6 grid grid-cols-2 gap-2 sm:grid-cols-4'>
        {SKELETON_KEYS.map((key) => (
          <Skeleton key={`metric-${key}`} className='h-16 w-full' />
        ))}
      </div>
      <div className='mt-6 space-y-3'>
        {SKELETON_KEYS.map((key) => (
          <Skeleton key={`section-${key}`} className='h-24 w-full' />
        ))}
      </div>
    </div>
  )
}
