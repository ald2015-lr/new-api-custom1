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
import { Suspense } from 'react'
import { useTranslation } from 'react-i18next'

import { sideDrawerContentClassName } from '@/components/drawer-layout'
import {
  ChunkLoadErrorNotice,
  LazyChunkBoundary,
} from '@/components/lazy-chunk-boundary'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { lazyWithRetry } from '@/lib/lazy-with-retry'

import { loadModelDetails } from '../lib/model-details-chunks'
import type { ModelDetailsContentProps } from './model-details'
import { ModelDetailsSkeleton } from './model-details-skeleton'

const ModelDetailsContent = lazyWithRetry(() =>
  loadModelDetails().then((module) => ({
    default: module.ModelDetailsContent,
  }))
)

export interface ModelDetailsDrawerProps extends ModelDetailsContentProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * The model details beside the model square. The drawer opens at once; its
 * content is a chunk of its own (prefetched on intent and at idle, see
 * model-details-chunks.ts) and shows a skeleton while that loads. A failed
 * load offers a retry, and the next opening loads it again.
 */
export function ModelDetailsDrawer(props: ModelDetailsDrawerProps) {
  const { t } = useTranslation()
  const { open, onOpenChange, ...contentProps } = props

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side='right'
        className={sideDrawerContentClassName(
          'sm:max-w-2xl lg:max-w-3xl xl:max-w-4xl 2xl:max-w-5xl'
        )}
      >
        <SheetHeader className='sr-only'>
          <SheetTitle>{props.model.model_name}</SheetTitle>
          <SheetDescription>{t('Model details')}</SheetDescription>
        </SheetHeader>
        <div className='flex-1 overflow-y-auto px-4 pt-11 pb-5 sm:px-6 sm:pt-12 sm:pb-6'>
          <LazyChunkBoundary fallback={<ChunkLoadErrorNotice />}>
            <Suspense fallback={<ModelDetailsSkeleton />}>
              <ModelDetailsContent {...contentProps} />
            </Suspense>
          </LazyChunkBoundary>
        </div>
      </SheetContent>
    </Sheet>
  )
}
