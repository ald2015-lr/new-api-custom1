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
import { ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import {
  MODERATION_CATEGORIES,
  MODERATION_DETAIL_LABELS,
  MODERATION_ERROR_CLASS_LABELS,
  MODERATION_PRESETS,
  MODERATION_SOURCE_LABELS,
  MODERATION_STATUS_LABELS,
  moderationLabel,
  type ModerationAudit,
  type ModerationAuditRule,
  type ModerationKeyFailure,
} from '@/lib/content-moderation'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { DetailRow, DetailSection } from './log-detail-layout'

function categoryLabel(value: string, t: (key: string) => string): string {
  const category = MODERATION_CATEGORIES.find((item) => item.value === value)
  return category ? t(category.labelKey) : value
}

function ruleText(
  rule: ModerationAuditRule,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  let name: string
  if (rule.kind === 'preset') {
    const preset = MODERATION_PRESETS.find((item) => item.id === rule.id)
    const presetName = preset
      ? `${t(preset.labelKey)} (${t(preset.language === 'en' ? 'English' : 'Chinese')})`
      : (rule.id ?? '')
    name = `${t('Built-in rule')}: ${presetName}`
  } else {
    // The editor numbers custom rules from 1.
    const index = typeof rule.index === 'number' ? rule.index + 1 : '?'
    name = t('Custom rule #{{index}}', { index })
    if (rule.name) name += `: ${rule.name}`
  }
  let type = rule.type
  if (rule.type === 'keyword') type = t('Keyword')
  else if (rule.type === 'regex') type = t('Regular expression')
  return type ? `${name} · ${type}` : name
}

function failureText(
  failure: { class: string; status?: number },
  t: (key: string) => string
): string {
  const text = moderationLabel(MODERATION_ERROR_CLASS_LABELS, failure.class, t)
  return failure.status ? `${text} (HTTP ${failure.status})` : text
}

/** The log row's channel, the only one whose name the row carries. */
type LogChannel = { id: number; name?: string | null }

function keyFailureText(
  failure: ModerationKeyFailure,
  channel: LogChannel | undefined,
  t: (key: string, options?: Record<string, unknown>) => string
): string {
  let channelText = t('Channel #{{id}}', { id: failure.channel_id })
  if (channel?.name && channel.id === failure.channel_id) {
    channelText += ` (${channel.name})`
  }
  // The attempt's place in the request's retry chain. Rows written before
  // the server sent it number the attempts by their retry index, which only
  // repeats under auto cross-group retry.
  const parts = [
    channelText,
    t('Attempt #{{index}}', {
      index: failure.attempt ?? failure.retry_index + 1,
    }),
  ]
  // Multi-key channels only; the channel's key list numbers keys from 1.
  if (typeof failure.multi_key_index === 'number') {
    parts.push(t('Key #{{index}}', { index: failure.multi_key_index + 1 }))
  }
  parts.push(failureText(failure, t))
  return parts.join(' · ')
}

/**
 * What content moderation did for the attempt of a log row: why it rejected
 * the request, or that it passed or let the request through after a failed
 * call. Administrators only; the server removes it for everyone else.
 */
export function ModerationDetailsSection(props: {
  audit: ModerationAudit
  channel?: LogChannel
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const audit = props.audit
  const categories = (audit.categories ?? [])
    .map((value) => categoryLabel(value, t))
    .join(', ')
  const scores = Object.entries(audit.scores ?? {})
    .sort(([, a], [, b]) => b - a)
    .map(([category, score]) => `${category} ${score.toFixed(4)}`)
    .join(', ')
  const inputTypes = Object.entries(audit.applied_input_types ?? {})
    .map(([category, types]) => `${category}: ${types.join(', ')}`)
    .join('; ')
  const notFullyChecked =
    audit.truncated === true ||
    audit.rule_scan_truncated === true ||
    (audit.images_skipped ?? 0) > 0 ||
    (audit.unchecked_parts ?? 0) > 0
  const error = audit.error ? failureText(audit.error, t) : ''
  const keyFailures = audit.key_failures ?? []
  const count = (value: number | undefined) =>
    value ? formatNumber(value, locale) : null
  // DetailRow breaks anywhere for IDs; sentences wrap between words and
  // break a word only when it does not fit on a line.
  const prose = 'break-normal wrap-anywhere'

  return (
    <DetailSection
      icon={<ShieldCheck className='size-3.5' aria-hidden='true' />}
      label={t('Content moderation')}
      variant={audit.status === 'rejected' ? 'danger' : 'default'}
    >
      <DetailRow
        label={t('Result')}
        value={moderationLabel(MODERATION_STATUS_LABELS, audit.status, t)}
      />
      {audit.source && (
        <DetailRow
          label={t('Source')}
          value={moderationLabel(MODERATION_SOURCE_LABELS, audit.source, t)}
        />
      )}
      {audit.detail && (
        <DetailRow
          label={t('Reason')}
          value={moderationLabel(MODERATION_DETAIL_LABELS, audit.detail, t)}
        />
      )}
      {categories && (
        <DetailRow
          label={t('Categories')}
          value={<span className={prose}>{categories}</span>}
        />
      )}
      {scores && <DetailRow label={t('Scores')} value={scores} mono />}
      {inputTypes && (
        <DetailRow label={t('Input types')} value={inputTypes} mono />
      )}
      {audit.rule && (
        <DetailRow
          label={t('Rule')}
          value={<span className={prose}>{ruleText(audit.rule, t)}</span>}
        />
      )}
      {audit.match && (
        <DetailRow label={t('Matched text')} value={audit.match} mono />
      )}
      {audit.error && (
        <DetailRow
          label={t('Error')}
          value={
            <span className={prose}>
              {error}
              {audit.error.message && (
                <span className='text-muted-foreground block'>
                  {audit.error.message}
                </span>
              )}
            </span>
          }
        />
      )}
      {keyFailures.length > 0 && (
        <DetailRow
          label={t('Unusable keys')}
          value={
            <ul className={cn(prose, 'space-y-0.5')}>
              {keyFailures.map((failure, index) => (
                <li
                  // eslint-disable-next-line react/no-array-index-key -- The attempts never change order, and rows written before they were numbered can repeat every other field.
                  key={`${index}-${failure.channel_id}`}
                >
                  {keyFailureText(failure, props.channel, t)}
                </li>
              ))}
            </ul>
          }
        />
      )}
      {audit.model && (
        <DetailRow label={t('OpenAI model')} value={audit.model} mono />
      )}
      {audit.id && (
        <DetailRow label={t('Moderation ID')} value={audit.id} mono />
      )}
      {audit.upstream_request_id && (
        <DetailRow
          label={t('Upstream Request ID')}
          value={audit.upstream_request_id}
          mono
        />
      )}
      {typeof audit.elapsed_ms === 'number' && (
        <DetailRow
          label={t('Moderation time')}
          value={t('{{ms}} ms', { ms: audit.elapsed_ms })}
        />
      )}
      {typeof audit.checked_chars === 'number' && (
        <DetailRow
          label={t('Characters checked')}
          value={formatNumber(audit.checked_chars, locale)}
        />
      )}
      {count(audit.cached_pieces) && (
        <DetailRow
          label={t('Cached pieces')}
          value={count(audit.cached_pieces)}
        />
      )}
      {count(audit.images_checked) && (
        <DetailRow
          label={t('Images checked')}
          value={count(audit.images_checked)}
        />
      )}
      {count(audit.images_skipped) && (
        <DetailRow
          label={t('Images skipped')}
          value={count(audit.images_skipped)}
        />
      )}
      {count(audit.unchecked_parts) && (
        <DetailRow
          label={t('Parts not checked')}
          value={count(audit.unchecked_parts)}
        />
      )}
      {notFullyChecked && (
        <p className='text-xs font-medium text-amber-600 dark:text-amber-400'>
          {t('Not fully checked')}
        </p>
      )}
    </DetailSection>
  )
}
