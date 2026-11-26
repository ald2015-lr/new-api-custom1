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
import { lazy, Suspense, type ComponentProps } from 'react'

import {
  ChunkLoadErrorNotice,
  LazyChunkBoundary,
} from '@/components/lazy-chunk-boundary'
import { Skeleton } from '@/components/ui/skeleton'

import {
  loadModelDetailsCharts,
  loadModelDetailsCodeSample,
} from '../lib/model-details-tab-chunks'
import type {
  LatencyTrendChart,
  UptimeTrendChart,
} from './model-details-charts'
import type { ModelDetailsCodeSample } from './model-details-code-sample'

const LatencyTrendChartImpl = lazy(() =>
  loadModelDetailsCharts().then((module) => ({
    default: module.LatencyTrendChart,
  }))
)
const UptimeTrendChartImpl = lazy(() =>
  loadModelDetailsCharts().then((module) => ({
    default: module.UptimeTrendChart,
  }))
)
const CodeSampleImpl = lazy(() =>
  loadModelDetailsCodeSample().then((module) => ({
    default: module.ModelDetailsCodeSample,
  }))
)

function ChartSkeleton() {
  return <Skeleton className='h-64 w-full sm:h-72' aria-busy='true' />
}

export function LazyLatencyTrendChart(
  props: ComponentProps<typeof LatencyTrendChart>
) {
  return (
    <LazyChunkBoundary fallback={<ChunkLoadErrorNotice />}>
      <Suspense fallback={<ChartSkeleton />}>
        <LatencyTrendChartImpl {...props} />
      </Suspense>
    </LazyChunkBoundary>
  )
}

export function LazyUptimeTrendChart(
  props: ComponentProps<typeof UptimeTrendChart>
) {
  return (
    <LazyChunkBoundary fallback={<ChunkLoadErrorNotice />}>
      <Suspense fallback={<ChartSkeleton />}>
        <UptimeTrendChartImpl {...props} />
      </Suspense>
    </LazyChunkBoundary>
  )
}

/** The sample as plain text, until (or if never) the highlighter loads. */
function PlainCodeSample(props: { code: string }) {
  return (
    <pre className='bg-muted/30 overflow-x-auto rounded-lg border p-3 font-mono text-xs leading-relaxed'>
      <code>{props.code}</code>
    </pre>
  )
}

export function LazyCodeSample(
  props: ComponentProps<typeof ModelDetailsCodeSample>
) {
  const plain = <PlainCodeSample code={props.code} />
  return (
    <LazyChunkBoundary fallback={plain}>
      <Suspense fallback={plain}>
        <CodeSampleImpl {...props} />
      </Suspense>
    </LazyChunkBoundary>
  )
}
