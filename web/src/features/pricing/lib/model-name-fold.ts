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

// ----------------------------------------------------------------------------
// Model name folding for the Model Square (display only: the names used for
// calls, billing and logs never change). Pure string rules, no React:
//   - version spelling: claude-opus-4-5 ≡ claude-opus-4.5, letter case, one
//     leading org/ segment
//   - dated snapshots: a trailing date (gpt-4o-mini-2024-07-18, tts-1-1106,
//     claude-opus-4-5-20251101 …) peeled off as a variant
//   - search: the folded forms of a typed query
// ----------------------------------------------------------------------------

const LINE_TAG_PATTERN = /^\[([^\]]+)\]/

/** The leading `[...]` line tag of a name ('' without one) and the rest. */
export function splitLineTag(name: string): { line: string; rest: string } {
  const tag = LINE_TAG_PATTERN.exec(name)
  return tag
    ? { line: tag[1], rest: name.slice(tag[0].length) }
    : { line: '', rest: name }
}

// ----------------------------------------------------------------------------
// Version spelling (a token rule without look-around, so the same rule works
// in Go's RE2)
// ----------------------------------------------------------------------------

/** A token ending in a 1-2 digit number (no leading zero) after letters, or that number alone. */
const MAJOR = /^(?:[a-z](?:[a-z0-9.]*[a-z])?)?(?:0|[1-9]\d?)$/
/** A bare 1-2 digit number without a leading zero. */
const MINOR = /^(?:0|[1-9]\d?)$/
const NUMBER = /^\d+$/

/**
 * Join tokens A-B as A.B when A ends in a 1-2 digit number (no leading zero;
 * the whole token or after letters), B is a bare 1-2 digit number (no leading
 * zero), the token after B is not a bare number and A is not a bare number
 * preceded by a bare number. Input is one lower-cased path segment.
 * `relaxed` (search queries only) drops the "token after B" condition, so a
 * typed `claude-opus-4-5-2025` reads as `claude-opus-4.5-2025`.
 */
export function foldVersions(segment: string, relaxed = false): string {
  const t = segment.split('-')
  const out: string[] = []
  for (let i = 0; i < t.length;) {
    const cur = t[i]
    if (
      i + 1 < t.length &&
      MAJOR.test(cur) &&
      MINOR.test(t[i + 1]) &&
      (relaxed || !(i + 2 < t.length && NUMBER.test(t[i + 2]))) &&
      !(i > 0 && NUMBER.test(cur) && NUMBER.test(t[i - 1]))
    ) {
      out.push(`${cur}.${t[i + 1]}`)
      i += 2
      continue
    }
    out.push(cur)
    i++
  }
  return out.join('-')
}

/**
 * Length of the one leading `org/` segment the spelling fold drops from a
 * line-tag-free name, or 0. Decided once per name on the whole name: exactly
 * one `/`, not starting with `@`, and the rest looks like a model id (has a
 * digit or a dash), so agentcore/search and openrouter/auto stay apart.
 */
export function orgPrefixLength(untagged: string): number {
  const slash = untagged.indexOf('/')
  if (
    slash <= 0 ||
    slash !== untagged.lastIndexOf('/') ||
    untagged.startsWith('@')
  ) {
    return 0
  }
  return /[-\d]/.test(untagged.slice(slash + 1)) ? slash + 1 : 0
}

/**
 * The spelling key of a line-tag-free name part: lower case, without the
 * first `orgLength` characters (the org/ prefix of the whole name), dash
 * versions folded in the last path segment. Identity when the fold is off.
 */
export function spellingKey(
  text: string,
  fold: boolean,
  orgLength = orgPrefixLength(text),
  relaxed = false
): string {
  if (!fold) return text
  const s = text.slice(orgLength).toLowerCase()
  const cut = s.lastIndexOf('/') + 1
  return s.slice(0, cut) + foldVersions(s.slice(cut), relaxed)
}

/**
 * Whether the dash at index `d` of `text` sits inside a version pair that the
 * fold joins (the `-` of `claude-opus-4-5`), ignoring what follows the pair:
 * the token before ends in a 1-2 digit number, the token after (up to the next
 * `-`, `@` or `:`) is a bare 1-2 digit number, and the token before is not a
 * bare number preceded by a bare number. Dashes before the last `/` never are.
 * A family base never ends at such a dash, so `claude-opus-4` does not catch
 * `claude-opus-4-5-20251101`.
 */
export function isVersionDash(text: string, d: number): boolean {
  const seg = text.lastIndexOf('/') + 1
  if (d < seg) return false
  const prev = text.lastIndexOf('-', d - 1)
  const left = text.slice(Math.max(prev, seg - 1) + 1, d).toLowerCase()
  const next = text.indexOf('-', d + 1)
  let right = text.slice(d + 1, next < 0 ? text.length : next)
  const stop = right.search(/[@:]/)
  if (stop >= 0) right = right.slice(0, stop)
  if (!MAJOR.test(left) || !MINOR.test(right)) return false
  if (prev >= seg && NUMBER.test(left)) {
    const before = text.slice(
      Math.max(text.lastIndexOf('-', prev - 1), seg - 1) + 1,
      prev
    )
    if (NUMBER.test(before)) return false
  }
  return true
}

// ----------------------------------------------------------------------------
// Dated snapshots
// ----------------------------------------------------------------------------

export type SnapshotFormat =
  | 'YYYY-MM-DD'
  | 'YYYY-MM'
  | 'MM-YYYY'
  | 'MM-DD'
  | 'YYYYMMDD'
  | 'YYYYMM'
  | 'YYMMDD'
  | 'MMDD'
  | 'YYMM'
  | '@YYYYMMDD'
  | '@YYMM-00N'
  | '@YYMM'
  | '@NNN'
  | '-00N'
  | 'latest'

export type SnapshotHit = {
  format: SnapshotFormat
  /** The date as written, without the separator before it. */
  label: string
  /** The name part before the date. */
  base: string
}

/** Sort key of a snapshot label: [group, a, b, c], compared element-wise. */
export type SnapshotSortKey = [number, number, number, number]

const validMD = (m: number, d: number) => m >= 1 && m <= 12 && d >= 1 && d <= 31
const isYear = (y: number) => y >= 2020 && y <= 2039
const isYY = (yy: number) => yy >= 23 && yy <= 39
const D2 = /^\d{2}$/
const D3_00N = /^00\d$/
const D4 = /^\d{4}$/
const D6 = /^\d{6}$/
const D8 = /^\d{8}$/
const HAS_LETTER = /[a-z]/i
/** -00N folds only for Gemini/Imagen/Veo names with an x.y version. */
const SCOPED_00N =
  /^(?:[^/]*\/)*(?:gemini|imagen|veo)-\d+\.\d+(?:-[a-z0-9.]+)*$/
const THINKING_TAIL = /(?:^|[-_])thinking$/
const MOE_ACTIVE_SIZE_TAIL = /-a\d+(?:\.\d+)?b$/

function yymm(tok: string): boolean {
  return (
    D4.test(tok) &&
    isYY(+tok.slice(0, 2)) &&
    +tok.slice(2) >= 1 &&
    +tok.slice(2) <= 12
  )
}

function mmdd(tok: string): boolean {
  return D4.test(tok) && validMD(+tok.slice(0, 2), +tok.slice(2))
}

function yyyymmdd(tok: string): boolean {
  return (
    D8.test(tok) &&
    isYear(+tok.slice(0, 4)) &&
    validMD(+tok.slice(4, 6), +tok.slice(6))
  )
}

/**
 * The trailing date-like suffix of `rest` (line tag already removed), or
 * null. Only the end of the name is read, and the first format whose test
 * matches is the only candidate: when a guard rejects it there is no date.
 * `latest` hits are returned with format 'latest'; the caller decides whether
 * they fold (only into a family other names on the page already have).
 */
export function dateSuffix(rest: string): SnapshotHit | null {
  // Only a colon in the last path segment blocks a date
  // (hf:deepseek-ai/DeepSeek-R1-0528 folds, ft:…:acme::id does not).
  if (rest.slice(rest.lastIndexOf('/') + 1).includes(':')) return null
  const at = rest.lastIndexOf('@')
  if (at > 0) {
    const base = rest.slice(0, at)
    const tail = rest.slice(at + 1)
    let hit: SnapshotHit | null = null
    if (yyyymmdd(tail)) hit = { format: '@YYYYMMDD', label: tail, base }
    else if (/^\d{4}-00\d$/.test(tail) && yymm(tail.slice(0, 4))) {
      hit = { format: '@YYMM-00N', label: tail, base }
    } else if (yymm(tail)) hit = { format: '@YYMM', label: tail, base }
    else if (/^\d{3}$/.test(tail)) hit = { format: '@NNN', label: tail, base }
    else if (tail.toLowerCase() === 'latest') {
      hit = { format: 'latest', label: 'latest', base }
    }
    return hit && guard(hit) ? hit : null
  }
  const t = rest.split('-')
  const n = t.length
  const join = (k: number) => t.slice(0, n - k).join('-')
  const tail = (k: number) => t.slice(n - k).join('-')
  let hit: SnapshotHit | null = null
  if (
    n >= 4 &&
    D4.test(t[n - 3]) &&
    D2.test(t[n - 2]) &&
    D2.test(t[n - 1]) &&
    isYear(+t[n - 3]) &&
    validMD(+t[n - 2], +t[n - 1])
  ) {
    hit = { format: 'YYYY-MM-DD', label: tail(3), base: join(3) }
  } else if (
    n >= 3 &&
    D4.test(t[n - 2]) &&
    D2.test(t[n - 1]) &&
    isYear(+t[n - 2]) &&
    +t[n - 1] >= 1 &&
    +t[n - 1] <= 12
  ) {
    hit = { format: 'YYYY-MM', label: tail(2), base: join(2) }
  } else if (
    n >= 3 &&
    D2.test(t[n - 2]) &&
    D4.test(t[n - 1]) &&
    +t[n - 2] >= 1 &&
    +t[n - 2] <= 12 &&
    isYear(+t[n - 1])
  ) {
    hit = { format: 'MM-YYYY', label: tail(2), base: join(2) }
  } else if (
    n >= 3 &&
    D2.test(t[n - 2]) &&
    D2.test(t[n - 1]) &&
    validMD(+t[n - 2], +t[n - 1]) &&
    (t[n - 2].startsWith('0') || t[n - 1].startsWith('0') || +t[n - 2] >= 10)
  ) {
    hit = { format: 'MM-DD', label: tail(2), base: join(2) }
  } else if (n >= 2) {
    const last = t[n - 1]
    if (yyyymmdd(last)) hit = { format: 'YYYYMMDD', label: last, base: join(1) }
    else if (
      D6.test(last) &&
      isYY(+last.slice(0, 2)) &&
      validMD(+last.slice(2, 4), +last.slice(4))
    ) {
      hit = { format: 'YYMMDD', label: last, base: join(1) }
    } else if (
      D6.test(last) &&
      isYear(+last.slice(0, 4)) &&
      +last.slice(4) >= 1 &&
      +last.slice(4) <= 12
    ) {
      hit = { format: 'YYYYMM', label: last, base: join(1) }
    } else if (mmdd(last)) hit = { format: 'MMDD', label: last, base: join(1) }
    else if (yymm(last)) hit = { format: 'YYMM', label: last, base: join(1) }
    else if (D3_00N.test(last)) {
      hit = { format: '-00N', label: last, base: join(1) }
    } else if (last.toLowerCase() === 'latest') {
      hit = { format: 'latest', label: 'latest', base: join(1) }
    }
  }
  return hit && guard(hit) ? hit : null
}

/** Guards shared by all formats. */
function guard(hit: SnapshotHit): boolean {
  const base = hit.base
  if (
    !base ||
    base.endsWith('-') ||
    base.endsWith('@') ||
    !HAS_LETTER.test(base)
  ) {
    return false
  }
  const lower = base.toLowerCase()
  // A 4-digit number right after -thinking is a thinking budget
  // (-thinking-1024), not a date; longer dates after -thinking
  // (-thinking-250715, -thinking-20251109) are vendor snapshots.
  if (
    (hit.format === 'MMDD' || hit.format === 'YYMM') &&
    THINKING_TAIL.test(lower)
  ) {
    return false
  }
  // A 4-digit or MM-DD date right after a MoE active size (-a22b-2507) is a
  // new weight set, not a snapshot of the April model.
  if (
    (hit.format === 'MMDD' ||
      hit.format === 'YYMM' ||
      hit.format === 'MM-DD') &&
    MOE_ACTIVE_SIZE_TAIL.test(lower)
  ) {
    return false
  }
  // Gemini: a 4-digit MMDD is a snapshot only after -exp / -preview; -1024 is
  // a thinking budget.
  if (
    hit.format === 'MMDD' &&
    /(?:^|\/)gemini-/.test(lower) &&
    !/-(?:exp|preview)$/.test(lower)
  ) {
    return false
  }
  // -00N is part of the product name except for Gemini/Imagen/Veo x.y names.
  if (hit.format === '-00N' && !SCOPED_00N.test(lower)) return false
  return true
}

/**
 * Sort key of a snapshot label: group 2 = has a year (y, m, d), 1 = a version
 * number (N), 0 = no year (m, d). Snapshots sort by it, newest first.
 */
export function snapshotSortKey(
  format: SnapshotFormat,
  label: string
): SnapshotSortKey {
  const n = (s: string) => Number.parseInt(s, 10)
  switch (format) {
    case 'YYYY-MM-DD':
      return [
        2,
        n(label.slice(0, 4)),
        n(label.slice(5, 7)),
        n(label.slice(8, 10)),
      ]
    case 'YYYYMMDD':
    case '@YYYYMMDD':
      return [
        2,
        n(label.slice(0, 4)),
        n(label.slice(4, 6)),
        n(label.slice(6, 8)),
      ]
    case 'YYMMDD':
      return [
        2,
        2000 + n(label.slice(0, 2)),
        n(label.slice(2, 4)),
        n(label.slice(4, 6)),
      ]
    case 'YYYYMM':
      return [2, n(label.slice(0, 4)), n(label.slice(4, 6)), 0]
    case 'YYYY-MM':
      return [2, n(label.slice(0, 4)), n(label.slice(5, 7)), 0]
    case 'MM-YYYY':
      return [2, n(label.slice(3, 7)), n(label.slice(0, 2)), 0]
    case 'YYMM':
    case '@YYMM':
      return [2, 2000 + n(label.slice(0, 2)), n(label.slice(2, 4)), 0]
    case '@YYMM-00N':
      return [
        2,
        2000 + n(label.slice(0, 2)),
        n(label.slice(2, 4)),
        n(label.slice(5)),
      ]
    case '-00N':
    case '@NNN':
      return [1, n(label), 0, 0]
    case 'MMDD':
      return [0, n(label.slice(0, 2)), n(label.slice(2, 4)), 0]
    case 'MM-DD':
      return [0, n(label.slice(0, 2)), n(label.slice(3, 5)), 0]
    default:
      return [0, 0, 0, 0]
  }
}

// ----------------------------------------------------------------------------
// Card title tie-break
// ----------------------------------------------------------------------------

/**
 * Vendors whose own API writes versions with dashes (claude-opus-4-5,
 * doubao-seed-1-6, nai-diffusion-4-5, kling-v2-1, mistral-medium-3-5; the
 * Volcengine Ark names the official metadata sync saves). Every other vendor
 * writes dots (gpt-4.1, gemini-2.5, glm-4.7, deepseek-v3.2).
 */
export const DASH_VERSION_VENDORS: ReadonlySet<string> = new Set([
  'Anthropic',
  '字节跳动',
  'NovelAI',
  '快手',
  'Mistral',
  'Volcengine Ark',
  'Volcengine Ark Coding Plan',
])

// ----------------------------------------------------------------------------
// Search
// ----------------------------------------------------------------------------

/**
 * The folded forms of a search query: the strict and the relaxed fold, with a
 * leading [tag] kept in lower case. A dated name is then found in the other
 * spelling, and a half-typed date keeps its results.
 */
export function queryForms(query: string): string[] {
  const { line, rest } = splitLineTag(query.trim())
  const prefix = line ? `[${line.toLowerCase()}]` : ''
  const forms = [
    prefix + spellingKey(rest, true),
    prefix + spellingKey(rest, true, orgPrefixLength(rest), true),
  ]
  return forms.filter((form, index) => form && forms.indexOf(form) === index)
}
