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
import { memo, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import {
  formatLatency,
  formatThroughput,
  getSuccessRateDotClass,
} from '@/features/performance-metrics/lib/format'
import type { SuccessRatePoint } from '@/features/performance-metrics/types'
import { cn } from '@/lib/utils'

export type ModelPerfBadgeData = {
  window_start?: number
  window_end?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_series?: SuccessRatePoint[]
  /** Requests in the window, when the summary reports it. */
  request_count?: number
}

export interface ModelPerfBadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  perf: ModelPerfBadgeData | undefined
  /**
   * One-line layout for dense lists: labels stay available to assistive
   * technology and tooltips but are not shown above each value.
   */
  compact?: boolean
}

const STATUS_SLOTS = Array.from({ length: 24 }, (_, slot) => slot)

export const ModelPerfBadge = memo(function ModelPerfBadge(
  props: ModelPerfBadgeProps
) {
  const { t } = useTranslation()
  const latencyText = formatLatency(props.perf?.avg_latency_ms ?? 0)
  const throughputText = formatThroughput(props.perf?.avg_tps ?? 0).replace(
    ' t/s',
    't/s'
  )
  const successRate = props.perf?.success_rate
  const hasSuccessRate =
    successRate != null &&
    Number.isFinite(successRate) &&
    successRate >= 0 &&
    successRate <= 100
  // Hourly points use the server window, including the current partial hour.
  // Hours without traffic stay gray. Slot 23 is the current, partial hour.
  const statusRates = useMemo(() => {
    const windowStart = props.perf?.window_start
    if (windowStart == null) return STATUS_SLOTS.map(() => undefined)
    const ratesByHour = new Map<number, number>()
    for (const point of props.perf?.recent_success_series ?? []) {
      ratesByHour.set(point.ts, point.success_rate)
    }
    return STATUS_SLOTS.map((slot) => {
      const hourStart = windowStart + slot * 3600
      return ratesByHour.get(hourStart)
    })
  }, [props.perf?.recent_success_series, props.perf?.window_start])

  const successRateText = hasSuccessRate ? `${successRate.toFixed(2)}%` : '—'
  const latencyValue = latencyText === '—' ? '—s' : latencyText
  const throughputValue = throughputText === '—' ? '—t/s' : throughputText
  const statusStrip = (
    <dd
      role='img'
      aria-label={t(
        'Recent success-rate samples; gray bars indicate missing data.'
      )}
      title={t('Recent success-rate samples; gray bars indicate missing data.')}
      className={cn(
        'flex h-3 items-center gap-px',
        props.compact ? 'w-[71px] shrink-0' : 'mt-1 w-24'
      )}
    >
      {STATUS_SLOTS.map((slot) => {
        const rate = statusRates[slot]
        return (
          <span
            key={slot}
            aria-hidden
            className={cn(
              'h-full shrink-0 rounded-xs',
              props.compact ? 'w-[2px]' : 'w-[3px]',
              rate != null && Number.isFinite(rate) && rate >= 0 && rate <= 100
                ? getSuccessRateDotClass(rate)
                : 'bg-muted-foreground/15'
            )}
          />
        )
      })}
    </dd>
  )

  if (props.compact) {
    return (
      <div
        aria-label={t('Performance metrics for the last 24 hours')}
        className={cn('flex min-w-0 items-center gap-2', props.className)}
      >
        <dl className='flex min-w-0 items-center gap-2 text-xs tabular-nums'>
          <div
            className='flex shrink-0 items-center gap-1.5'
            title={t(
              'Success rate excludes business rejections and includes the current partial hour.'
            )}
          >
            <dt className='sr-only'>{t('Status')}</dt>
            {statusStrip}
            <dd className='min-w-[2.75rem] font-mono'>{successRateText}</dd>
          </div>
          <div title={t('Average latency')} className='shrink-0'>
            <dt className='sr-only'>{t('Latency short')}</dt>
            <dd className='min-w-10 font-mono whitespace-nowrap'>
              {latencyValue}
            </dd>
          </div>
          <div title={t('Throughput')} className='shrink-0'>
            <dt className='sr-only'>{t('Throughput short')}</dt>
            <dd className='min-w-[3.25rem] font-mono whitespace-nowrap'>
              {throughputValue}
            </dd>
          </div>
        </dl>
        {props.children}
      </div>
    )
  }

  return (
    <div
      aria-label={t('Performance metrics for the last 24 hours')}
      className={cn(
        // A narrow card moves the trailing action below instead of over the values.
        'flex w-full min-w-0 flex-wrap items-center justify-between gap-x-3 gap-y-1',
        props.className
      )}
    >
      <dl className='flex min-w-0 items-start gap-5 text-xs tabular-nums'>
        <div className='w-24 shrink-0'>
          <dt
            title={t(
              'Success rate excludes business rejections and includes the current partial hour.'
            )}
            className='text-muted-foreground flex items-center justify-between gap-1 text-[11px] leading-4'
          >
            <span>{t('Status')}</span>
            <span className='font-mono'>{successRateText}</span>
          </dt>
          {statusStrip}
        </div>
        <div title={t('Average latency')} className='shrink-0'>
          <dt className='text-muted-foreground text-[11px] leading-4'>
            {t('Latency short')}
          </dt>
          <dd className='mt-1 font-mono whitespace-nowrap'>{latencyValue}</dd>
        </div>
        <div title={t('Throughput')} className='shrink-0'>
          <dt className='text-muted-foreground text-[11px] leading-4'>
            {t('Throughput short')}
          </dt>
          <dd className='mt-1 font-mono whitespace-nowrap'>
            {throughputValue}
          </dd>
        </div>
      </dl>
      {props.children}
    </div>
  )
})
