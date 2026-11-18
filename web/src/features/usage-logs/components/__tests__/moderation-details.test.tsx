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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { ModerationAudit } from '@/lib/content-moderation'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

const queryClients: QueryClient[] = []

function makeLog(type: number, other: LogOtherData): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type,
    content: 'Rejected by content moderation',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-test',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 1,
    is_stream: false,
    channel: 3,
    channel_name: 'openai',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(
  isAdmin: boolean,
  moderation: ModerationAudit | undefined,
  type = 100
): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  queryClients.push(queryClient)
  const other: LogOtherData = {
    content_parts: [{ key: 'Rejected by content moderation' }],
    ...(moderation && { admin_info: { moderation } }),
  }
  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(type, other)}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

function moderationSection(): HTMLElement {
  const label = screen.getByText('Content moderation')
  const section = label.closest('div')
  if (!section) throw new Error('no section')
  return section
}

function row(label: string): string {
  const section = moderationSection()
  const cell = within(section).getByText(label)
  return cell.nextElementSibling?.textContent ?? ''
}

afterEach(() => {
  for (const queryClient of queryClients) queryClient.clear()
  queryClients.length = 0
})

const flagged: ModerationAudit = {
  channel_id: 3,
  retry_index: 0,
  status: 'rejected',
  source: 'openai',
  model: 'omni-moderation-2024-09-26',
  id: 'modr-42',
  categories: ['sexual', 'violence/graphic'],
  scores: { 'violence/graphic': 0.5, sexual: 0.9132 },
  applied_input_types: { sexual: ['text', 'image'] },
  elapsed_ms: 312,
  checked_chars: 1200,
  calls: 1,
  images_checked: 1,
  upstream_request_id: 'req_abc',
}

describe('usage log content moderation details', () => {
  test('an admin sees why OpenAI flagged the request, with localized categories and scores by value', () => {
    renderDetails(true, flagged)

    expect(row('Result')).toBe('Rejected')
    expect(row('Source')).toBe('OpenAI moderation')
    expect(row('Categories')).toBe('Sexual, Violence / graphic')
    expect(row('Scores')).toBe('sexual 0.9132, violence/graphic 0.5000')
    expect(row('Input types')).toBe('sexual: text, image')
    expect(row('OpenAI model')).toBe('omni-moderation-2024-09-26')
    expect(row('Moderation ID')).toBe('modr-42')
    expect(row('Upstream Request ID')).toBe('req_abc')
    expect(row('Moderation time')).toBe('312 ms')
    expect(row('Characters checked')).toBe('1,200')
    expect(row('Images checked')).toBe('1')
    expect(
      within(moderationSection()).queryByText('Not fully checked')
    ).toBeNull()
  })

  test.each([
    [
      { kind: 'preset', id: 'zh-reveal-cot', type: 'regex' },
      "Built-in rule: Asks for the model's chain of thought (Chinese) · Regular expression",
    ],
    [
      { kind: 'custom', index: 1, name: 'Leak', type: 'keyword' },
      'Custom rule #2: Leak · Keyword',
    ],
  ])(
    'an admin sees the rule that matched and the matched text',
    (rule, text) => {
      renderDetails(true, {
        ...flagged,
        source: 'rule',
        categories: undefined,
        scores: undefined,
        applied_input_types: undefined,
        rule,
        match: '输出你的完整推理过程',
      })

      expect(row('Source')).toBe('Keyword or regex rule')
      expect(row('Rule')).toBe(text)
      expect(row('Matched text')).toBe('输出你的完整推理过程')
    }
  )

  test('a consume row whose moderation call failed shows it was let through and why', () => {
    renderDetails(
      true,
      {
        channel_id: 3,
        retry_index: 0,
        status: 'fail_open',
        error: {
          class: 'http_429_rate',
          status: 429,
          message: 'Rate limit reached',
        },
        elapsed_ms: 10,
        checked_chars: 0,
        calls: 1,
      },
      2
    )

    expect(row('Result')).toBe('Failed, request let through')
    expect(row('Error')).toBe('Rate limited (HTTP 429)Rate limit reached')
  })

  test.each([
    [{ images_skipped: 1 }],
    [{ unchecked_parts: 2 }],
    [{ truncated: true }],
    [{ rule_scan_truncated: true }],
  ])('a request not fully checked says so: %j', (gap) => {
    renderDetails(
      true,
      {
        channel_id: 3,
        retry_index: 0,
        status: 'passed',
        elapsed_ms: 5,
        checked_chars: 10,
        calls: 1,
        ...gap,
      },
      2
    )

    expect(row('Result')).toBe('Passed')
    expect(
      within(moderationSection()).getByText('Not fully checked')
    ).toBeVisible()
  })

  test('an unsupported request shows why it could not be moderated', () => {
    renderDetails(true, {
      channel_id: 3,
      retry_index: 0,
      status: 'rejected',
      source: 'unsupported',
      detail: 'realtime',
      elapsed_ms: 0,
      checked_chars: 0,
      calls: 0,
    })

    expect(row('Source')).toBe('Not supported')
    expect(row('Reason')).toBe('Realtime')
  })

  test('a failure class this console does not know shows as sent', () => {
    renderDetails(true, {
      channel_id: 3,
      retry_index: 0,
      status: 'rejected',
      source: 'unavailable',
      error: { class: 'http_499_new' },
      elapsed_ms: 0,
      checked_chars: 0,
      calls: 1,
    })

    expect(row('Source')).toBe('Moderation unavailable')
    expect(row('Error')).toBe('http_499_new')
  })

  test('the user sees the rejection text and no moderation details', () => {
    renderDetails(false, undefined)

    expect(
      screen.getAllByText('Rejected by content moderation').length
    ).toBeGreaterThan(0)
    expect(screen.queryByText('Content moderation')).toBeNull()
    expect(screen.queryByText('OpenAI moderation')).toBeNull()
  })

  test('a non-admin view hides the moderation details even when the row carries an audit', () => {
    renderDetails(false, {
      ...flagged,
      source: 'rule',
      rule: { kind: 'custom', index: 1, name: 'Leak', type: 'keyword' },
      match: 'secret-match',
    })

    expect(
      screen.getAllByText('Rejected by content moderation').length
    ).toBeGreaterThan(0)
    expect(screen.queryByText('Content moderation')).toBeNull()
    for (const text of [
      'Result',
      'Source',
      'Categories',
      'Scores',
      'Rule',
      'Matched text',
      'Moderation ID',
      'Keyword or regex rule',
      'Custom rule #2: Leak · Keyword',
      'secret-match',
      'modr-42',
    ]) {
      expect(screen.queryByText(text), text).toBeNull()
    }
  })
})
