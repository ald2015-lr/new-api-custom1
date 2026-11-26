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
import type { PricingModel } from '../types'
import {
  DASH_VERSION_VENDORS,
  dateSuffix,
  isVersionDash,
  orgPrefixLength,
  snapshotSortKey,
  spellingKey,
  splitLineTag,
  type SnapshotFormat,
  type SnapshotSortKey,
} from './model-name-fold'

// ----------------------------------------------------------------------------
// Model variant grouping (display only: model names never change)
// ----------------------------------------------------------------------------

export type ModelVariantRule = {
  suffix: string
  label: string
  description?: string
}

/** Which filter sections the Model Square sidebar (and mobile drawer) shows. */
export type ModelSquareFilterSections = {
  groups: boolean
  vendors: boolean
  tags: boolean
  quota_types: boolean
  endpoint_types: boolean
}

/** Model tags and endpoint types are hidden unless an admin shows them. */
export const DEFAULT_FILTER_SECTIONS: ModelSquareFilterSections = {
  groups: true,
  vendors: true,
  tags: false,
  quota_types: true,
  endpoint_types: false,
}

export const ALL_FILTER_SECTIONS: ModelSquareFilterSections = {
  groups: true,
  vendors: true,
  tags: true,
  quota_types: true,
  endpoint_types: true,
}

export type ModelSquareSettings = {
  variant_grouping_enabled: boolean
  /**
   * Version spelling, letter case and one leading org/ segment: the family
   * key treats claude-opus-4-5 and claude-opus-4.5 alike.
   */
  version_spelling_fold_enabled: boolean
  /** A trailing date (and a contextual -latest) becomes a variant. */
  snapshot_fold_enabled: boolean
  variant_rules: ModelVariantRule[]
  /**
   * Base models named by the admin: every `<base>-<anything>` model joins the
   * base's family, even when no rule covers its suffix.
   */
  family_bases: string[]
  filter_sections: ModelSquareFilterSections
}

/**
 * Built-in variant rules, mirrored from the backend defaults. The order is the
 * display order of variants inside a family. Generic effort suffixes such as
 * -high or -max are left out on purpose: they would fold distinct models
 * (gpt-5.1-codex-max, qwen-max) into one family. Admins can add them.
 */
export const DEFAULT_MODEL_VARIANT_RULES: readonly ModelVariantRule[] = [
  { suffix: '-thinking', label: 'thinking', description: '' },
  { suffix: '-thinking-minimal', label: 'thinking-minimal', description: '' },
  { suffix: '-thinking-low', label: 'thinking-low', description: '' },
  { suffix: '-thinking-medium', label: 'thinking-medium', description: '' },
  { suffix: '-thinking-high', label: 'thinking-high', description: '' },
  { suffix: '-thinking-xhigh', label: 'thinking-xhigh', description: '' },
  { suffix: '-thinking-max', label: 'thinking-max', description: '' },
  {
    suffix: '-nothinking',
    label: 'nothinking',
    description: '会思考，但不返回思考内容',
  },
  {
    suffix: '-128',
    label: '128',
    description: '思考预算 128（最低思考强度）',
  },
]

export const MODEL_VARIANT_RULE_LIMITS = {
  maxRules: 100,
  maxSuffixLength: 64,
  maxLabelLength: 64,
  maxDescriptionLength: 200,
} as const

export const MODEL_FAMILY_BASE_LIMITS = {
  maxBases: 500,
  maxBaseLength: 128,
} as const

/** Display order of free-form variant names under a configured base. */
const EFFORT_ORDER = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']

/** Lines listed first, in this order; other lines follow in code-point order. */
const PREFERRED_LINE_ORDER = ['按次', '按量']

/** Code-point order: the same in every browser locale. */
function codePoint(a: string, b: string): number {
  if (a < b) return -1
  return a > b ? 1 : 0
}

export type ParsedModelName = {
  name: string
  /** Text inside the leading `[...]` tag, or '' when the name has none. */
  line: string
  /**
   * Base spelling: the name without its line tag, peeled suffixes and date,
   * in its original case (an `org/` prefix is kept).
   */
  spelling: string
  /**
   * Family key: the folded spelling (lower case, dotted versions, no org/
   * prefix) while the version fold is on, or the matched family base.
   */
  key: string
  /** Stripped rule suffixes as written in the name, in name order. */
  suffixes: string[]
  /** Rule index of each stripped suffix, in name order. */
  ruleIndexes: number[]
  /**
   * The rest of the name after a configured family base and before any rule
   * suffix or date (`low` in `gemini-4-argon-low`); '' otherwise.
   */
  extra: string
  /**
   * The peeled snapshot (the dash date when a name also has a Vertex `@NNN`
   * version); its label is the date as written.
   */
  date?: { format: SnapshotFormat; label: string; sortKey: SnapshotSortKey }
  /** Labels in name order joined with ` · `; '' for the standard variant. */
  label: string
  /** Non-empty rule descriptions joined with `；`. */
  description: string
  /** The folded spelling of the whole name without its line tag (search). */
  foldedName: string
  /**
   * Search text: `[line]` in lower case, the folded spelling and the peeled
   * tail (with the snapshot fold off, the spelling is folded without its
   * trailing date).
   */
  searchText: string
}

export type ModelVariant = {
  model: PricingModel
  parsed: ParsedModelName
}

export type ModelFamilyLine = {
  line: string
  variants: ModelVariant[]
}

export type ModelFamily = {
  key: string
  /**
   * A real member spelling (the base spelling most members use), or a
   * member's full name when no member is the plain base.
   */
  title: string
  titleIsFullName: boolean
  /** Other plain member spellings, best first (the card's "Also written as"). */
  alsoWrittenAs: string[]
  /** The displayed member the card describes: icon, description, tags. */
  lead: PricingModel
  /** The lead's vendor, else the most common vendor of the displayed members. */
  vendor?: { name: string; icon?: string }
  /** Members in the order of the incoming (filtered and sorted) list. */
  models: PricingModel[]
  /** Lines in display order, each with its variants in display order. */
  lines: ModelFamilyLine[]
  /** All variants in display order (lines flattened). */
  variants: ModelVariant[]
  /** Distinct variant labels in display order ('' = standard). */
  variantLabels: string[]
}

export type VariantRuleIssue = {
  index: number
  field: 'suffix' | 'label' | 'description'
  /** i18n key */
  message: string
  values?: Record<string, number>
}

/** The model name without its leading `[...]` line tag. */
export function stripLineTag(name: string): string {
  return splitLineTag(name).rest
}

// ----------------------------------------------------------------------------
// Parse one name
// ----------------------------------------------------------------------------

type BaseMatch = { base: string; cut: number; exact: boolean }

type Peel =
  | { kind: 'rule'; index: number; text: string }
  | { kind: 'date'; format: SnapshotFormat; label: string }

const NO_BASES: ReadonlySet<string> = new Set()
const NEVER = () => false

/** Family bases as the parser compares them: folded like names when the fold is on. */
export function normalizeFamilyBases(
  bases: readonly string[],
  versionSpellingFold: boolean
): ReadonlySet<string> {
  return new Set(bases.map((base) => spellingKey(base, versionSpellingFold)))
}

/**
 * The configured family base `rest` is (exact) or starts with at a dash
 * (prefix, longest first). With the version fold on, bases and prefixes are
 * compared folded and a prefix never ends at a version dash (isVersionDash),
 * so `claude-opus-4` does not catch `claude-opus-4-5-20251101` while
 * `claude-haiku-4-5` catches `claude-haiku-4-5-20251001` with any switch.
 * Bases never end with `-`, so a few set lookups per name suffice, however
 * many bases there are.
 */
export function matchFamilyBase(
  rest: string,
  org: number,
  bases: ReadonlySet<string>,
  fold: boolean
): BaseMatch | undefined {
  if (bases.size === 0) return undefined
  const o = Math.min(org, rest.length)
  const norm = (s: string) => spellingKey(s, fold, Math.min(o, s.length))
  const whole = norm(rest)
  if (bases.has(whole)) return { base: whole, cut: rest.length, exact: true }
  for (
    let d = rest.lastIndexOf('-');
    d > (fold ? o : 0);
    d = rest.lastIndexOf('-', d - 1)
  ) {
    if (fold && isVersionDash(rest, d)) continue
    const key = norm(rest.slice(0, d))
    if (bases.has(key)) return { base: key, cut: d, exact: false }
  }
  return undefined
}

export type ParseModelNameOptions = {
  rules: readonly ModelVariantRule[]
  /** Family bases from normalizeFamilyBases (with the same fold setting). */
  familyBases?: ReadonlySet<string>
  versionSpellingFold: boolean
  snapshotFold: boolean
  /**
   * Whether a `-latest` / `@latest` alias whose base has this key folds;
   * never by default (only the catalogue index knows the other names).
   */
  allowLatest?: (baseKey: string) => boolean
}

/**
 * Parse a model name into its line, base spelling, family key and variant.
 *
 * Peels from the right, alternating: the longest matching suffix rule (any
 * number of times), then one trailing date (plus at most one Vertex `@NNN`
 * version), until neither applies, peeling would leave an empty base or one
 * ending in `-`, or it would cut into a configured family base. With both
 * folds off this is exactly the grouping without folds: the key is the name
 * without its tag and rule suffixes, as written.
 */
export function parseModelName(
  name: string,
  options: ParseModelNameOptions
): ParsedModelName {
  const rules = options.rules
  const bases = options.familyBases ?? NO_BASES
  const fold = options.versionSpellingFold
  const allowLatest = options.allowLatest ?? NEVER
  const { line, rest: untagged } = splitLineTag(name)
  const org = orgPrefixLength(untagged)
  const norm = (s: string) => spellingKey(s, fold, Math.min(org, s.length))
  const endsWith = (s: string, suffix: string) =>
    fold ? s.toLowerCase().endsWith(suffix.toLowerCase()) : s.endsWith(suffix)

  let rest = untagged
  const peels: Peel[] = [] // right to left while peeling
  let match: BaseMatch | undefined
  let dateUsed = false
  /** A peel may not cut into the matched base. */
  const keepsBase = (candidate: string) =>
    !match || candidate.length >= match.cut

  for (;;) {
    if (!match) {
      match = matchFamilyBase(rest, org, bases, fold)
      if (match?.exact) break // an exact base is never cut into
    }
    let step: Peel | undefined
    let next = ''
    // The longest matching rule that keeps the base (ties: the earlier
    // rule); when it would leave '' or a trailing '-', peeling stops.
    let best = -1
    for (let index = 0; index < rules.length; index++) {
      const suffix = rules[index].suffix
      if (!suffix || !endsWith(rest, suffix)) continue
      if (!keepsBase(rest.slice(0, rest.length - suffix.length))) continue
      if (best < 0 || suffix.length > rules[best].suffix.length) best = index
    }
    if (best >= 0) {
      next = rest.slice(0, rest.length - rules[best].suffix.length)
      if (!next || next.endsWith('-')) break
      step = { kind: 'rule', index: best, text: rest.slice(next.length) }
    } else if (options.snapshotFold && !dateUsed) {
      const hit = dateSuffix(rest)
      if (
        hit &&
        keepsBase(hit.base) &&
        (hit.format !== 'latest' || allowLatest(norm(hit.base)))
      ) {
        next = hit.base
        step = { kind: 'date', format: hit.format, label: hit.label }
        // A Vertex @NNN version does not use up the name's one date
        // (mistral-small-2503@001).
        if (hit.format !== '@NNN') dateUsed = true
      }
    }
    if (!step) break
    peels.push(step)
    rest = next
  }

  const spelling = rest
  const folded = norm(rest)
  let key = folded
  let extra = ''
  if (match) {
    key = match.base
    extra =
      match.exact || rest.length === match.cut
        ? ''
        : folded.slice(match.base.length + 1)
  }
  peels.reverse() // name order
  const rulePeels = peels.filter(
    (peel): peel is Extract<Peel, { kind: 'rule' }> => peel.kind === 'rule'
  )
  const datePeels = peels.filter(
    (peel): peel is Extract<Peel, { kind: 'date' }> => peel.kind === 'date'
  )
  const labels = peels.map((peel) =>
    peel.kind === 'rule' ? rules[peel.index].label : peel.label
  )
  if (extra) labels.unshift(extra)
  const datePeel =
    datePeels.find((peel) => peel.format !== '@NNN') ?? datePeels[0]
  // While the snapshot fold is off the date stays in the spelling, so a
  // version written right before it keeps its dash in the key
  // (claude-opus-4-5-20251101). The search text still folds the spelling
  // without that date, so a dotted query (claude-opus-4.5) finds the name.
  let searchBase = spelling
  if (fold && !options.snapshotFold) {
    const hit = dateSuffix(spelling)
    if (hit && hit.format !== 'latest') searchBase = hit.base
  }

  return {
    name,
    line,
    spelling,
    key,
    suffixes: rulePeels.map((peel) => peel.text),
    ruleIndexes: rulePeels.map((peel) => peel.index),
    extra,
    date: datePeel
      ? {
          format: datePeel.format,
          label: datePeel.label,
          sortKey: snapshotSortKey(datePeel.format, datePeel.label),
        }
      : undefined,
    label: labels.join(' · '),
    description: rulePeels
      .map((peel) => rules[peel.index].description?.trim() ?? '')
      .filter(Boolean)
      .join('；'),
    foldedName: norm(untagged),
    searchText:
      (line ? `[${line.toLowerCase()}]` : '') +
      (fold
        ? spellingKey(searchBase, true, Math.min(org, searchBase.length))
        : searchBase.toLowerCase()) +
      untagged.slice(searchBase.length).toLowerCase(),
  }
}

/** Plain: nothing peeled and no family-base extra (the name is its spelling). */
function isPlain(parsed: ParsedModelName): boolean {
  return parsed.suffixes.length === 0 && !parsed.date && !parsed.extra
}

// ----------------------------------------------------------------------------
// Catalogue index: two parse passes (-latest), then whole-catalogue titles
// ----------------------------------------------------------------------------

export type FamilyTitle = {
  title: string
  /** True when no member is the plain base (the title is a member's full name). */
  fullName: boolean
  /** The model the title names. */
  titleModel: string
  /** Other spellings that are some member's whole untagged name, best first. */
  alsoWrittenAs: string[]
  /** Distinct base spellings, best first. */
  spellings: string[]
  /**
   * Every member name in lead order: the title model first, then the other
   * members of the title spelling, then the other spellings in rank order;
   * inside a spelling plain members first (line order, then name), then the
   * rest (fewest suffix-rule variants, variant order, line order, name).
   */
  order: string[]
}

export type ModelSquareIndex = {
  settings: ModelSquareSettings
  byName: Map<string, ParsedModelName>
  /** Title of every family key, chosen over all its catalogue members. */
  titles: Map<string, FamilyTitle>
}

const LATEST_KEY = /(?:^|[-@])latest$/i

function parseOptions(settings: ModelSquareSettings): ParseModelNameOptions {
  return {
    rules: settings.variant_rules,
    familyBases: normalizeFamilyBases(
      settings.family_bases,
      settings.version_spelling_fold_enabled
    ),
    versionSpellingFold: settings.version_spelling_fold_enabled,
    snapshotFold: settings.snapshot_fold_enabled,
  }
}

function groupByKey(
  variants: Iterable<ModelVariant>
): Map<string, ModelVariant[]> {
  const byKey = new Map<string, ModelVariant[]>()
  for (const variant of variants) {
    const list = byKey.get(variant.parsed.key)
    if (list) list.push(variant)
    else byKey.set(variant.parsed.key, [variant])
  }
  return byKey
}

/**
 * Parse every model of the catalogue once. Two passes: a -latest alias folds
 * only into a family key that other (non-latest) names of the catalogue have,
 * and only when that key carries a digit or the family has a dated member.
 * Then every family's title is chosen over all its catalogue members, so a
 * card keeps its name while the user searches or filters.
 */
export function buildModelSquareIndex(
  models: readonly PricingModel[],
  settings: ModelSquareSettings
): ModelSquareIndex {
  const options = parseOptions(settings)
  const pass1 = new Map<string, ParsedModelName>()
  for (const model of models) {
    const name = model.model_name || ''
    if (!pass1.has(name)) pass1.set(name, parseModelName(name, options))
  }
  const families = new Map<string, boolean>() // key -> has a dated member
  for (const parsed of pass1.values()) {
    if (LATEST_KEY.test(parsed.key)) continue
    families.set(
      parsed.key,
      (families.get(parsed.key) ?? false) || parsed.date !== undefined
    )
  }
  const allowLatest = (key: string) =>
    families.has(key) && (/\d/.test(key) || families.get(key) === true)
  const byName = new Map<string, ParsedModelName>()
  for (const [name, parsed] of pass1) {
    byName.set(
      name,
      /latest/i.test(name)
        ? parseModelName(name, { ...options, allowLatest })
        : parsed
    )
  }
  const members: ModelVariant[] = []
  for (const model of models) {
    const parsed = byName.get(model.model_name || '')
    if (parsed) members.push({ model, parsed })
  }
  const titles = new Map<string, FamilyTitle>()
  const byKey = groupByKey(members)
  for (const [key, members] of byKey) {
    titles.set(key, chooseFamilyTitle(members))
  }
  return { settings, byName, titles }
}

// ----------------------------------------------------------------------------
// Ordering of variants and lines
// ----------------------------------------------------------------------------

function compareRuleIndexes(a: number[], b: number[]): number {
  const length = Math.min(a.length, b.length)
  for (let index = 0; index < length; index++) {
    if (a[index] !== b[index]) return a[index] - b[index]
  }
  return a.length - b.length
}

/**
 * Variants with no free-form name come first (standard, then rule-only ones
 * such as nothinking in rule order), then known effort names in order, then
 * the rest in code-point order.
 */
function extraRank(extra: string): number {
  if (!extra) return -1
  const rank = EFFORT_ORDER.indexOf(extra)
  return rank < 0 ? EFFORT_ORDER.length : rank
}

/** No date (0), then -latest (1), then dated snapshots (2). */
function dateRank(parsed: ParsedModelName): number {
  if (!parsed.date) return 0
  return parsed.date.format === 'latest' ? 1 : 2
}

/**
 * Named variants first (standard, effort names, rule order), then -latest,
 * then snapshots newest first, each snapshot's own suffix variants right after
 * it. Year-less dates (MMDD) follow the dated ones.
 */
export function compareVariants(a: ModelVariant, b: ModelVariant): number {
  const pa = a.parsed
  const pb = b.parsed
  const rank = dateRank(pa) - dateRank(pb)
  if (rank) return rank
  if (pa.date && pb.date && dateRank(pa) === 2) {
    for (let index = 0; index < 4; index++) {
      const order = pb.date.sortKey[index] - pa.date.sortKey[index]
      if (order) return order
    }
  }
  return (
    extraRank(pa.extra) - extraRank(pb.extra) ||
    codePoint(pa.extra, pb.extra) ||
    compareRuleIndexes(pa.ruleIndexes, pb.ruleIndexes) ||
    codePoint(pa.label, pb.label) ||
    codePoint(pa.name, pb.name)
  )
}

function lineRank(line: string): number {
  if (!line) return -1
  const preferred = PREFERRED_LINE_ORDER.indexOf(line)
  return preferred < 0 ? PREFERRED_LINE_ORDER.length : preferred
}

/** Untagged first, then 按次, 按量, then other lines in code-point order. */
export function compareLines(a: string, b: string): number {
  return lineRank(a) - lineRank(b) || codePoint(a, b)
}

// ----------------------------------------------------------------------------
// Card title, other spellings and lead
// ----------------------------------------------------------------------------

/** `org/model`: exactly one slash, not first, not a Cloudflare `@cf/` path. */
function hasOrgPrefix(spelling: string): boolean {
  const slash = spelling.indexOf('/')
  return (
    slash > 0 &&
    slash === spelling.lastIndexOf('/') &&
    !spelling.startsWith('@')
  )
}

function hasDottedVersion(spelling: string): boolean {
  return /\d\.\d/.test(spelling.slice(spelling.lastIndexOf('/') + 1))
}

/** The vendor most members carry (ties: code-point order); '' without any. */
function majorityVendor(members: readonly ModelVariant[]): string {
  const counts = new Map<string, number>()
  for (const { model } of members) {
    if (model.vendor_name) {
      counts.set(model.vendor_name, (counts.get(model.vendor_name) ?? 0) + 1)
    }
  }
  let best = ''
  let bestCount = 0
  for (const [name, count] of counts) {
    if (count > bestCount || (count === bestCount && name < best)) {
      best = name
      bestCount = count
    }
  }
  return best
}

type RankedSpelling = {
  spelling: string
  variants: ModelVariant[]
  count: number
  untagged: number
  noPrefix: number
  apiForm: number
  lower: number
}

function compareRankedSpellings(a: RankedSpelling, b: RankedSpelling): number {
  return (
    b.count - a.count ||
    b.untagged - a.untagged ||
    b.noPrefix - a.noPrefix ||
    b.apiForm - a.apiForm ||
    b.lower - a.lower ||
    codePoint(a.spelling, b.spelling)
  )
}

/** Suffix-rule variants of a member: rule suffixes plus a family-base extra. */
function variantCount(variant: ModelVariant): number {
  return variant.parsed.ruleIndexes.length + (variant.parsed.extra ? 1 : 0)
}

/** Fewest suffix-rule variants, then variant order (alias before snapshots, newest first), then line. */
function compareLeadCandidates(a: ModelVariant, b: ModelVariant): number {
  return (
    variantCount(a) - variantCount(b) ||
    compareVariants(a, b) ||
    compareLines(a.parsed.line, b.parsed.line)
  )
}

/**
 * Card title of one family over all its catalogue members. Candidates are
 * base spellings that are some member's whole untagged name. Rank: most
 * members; a plain (or, without candidates, any) member on the untagged line;
 * no org/ prefix; the vendor's API form (dashes for DASH_VERSION_VENDORS,
 * dots otherwise); all lower case; code-point order. Without any candidate
 * the title is the untagged full name of the best member of the best-ranked
 * spelling.
 */
export function chooseFamilyTitle(
  members: readonly ModelVariant[]
): FamilyTitle {
  const bySpelling = new Map<string, ModelVariant[]>()
  for (const member of members) {
    const list = bySpelling.get(member.parsed.spelling)
    if (list) list.push(member)
    else bySpelling.set(member.parsed.spelling, [member])
  }
  const entries = [...bySpelling.entries()]
  const plainSpellings = entries.filter(([, variants]) =>
    variants.some((variant) => isPlain(variant.parsed))
  )
  const fallback = plainSpellings.length === 0
  const dashes = DASH_VERSION_VENDORS.has(majorityVendor(members))
  const rank = ([spelling, variants]: [
    string,
    ModelVariant[],
  ]): RankedSpelling => ({
    spelling,
    variants,
    count: variants.length,
    untagged: variants.some(
      (variant) =>
        variant.parsed.line === '' && (fallback || isPlain(variant.parsed))
    )
      ? 1
      : 0,
    noPrefix: hasOrgPrefix(spelling) ? 0 : 1,
    apiForm: hasDottedVersion(spelling) === !dashes ? 1 : 0,
    lower: spelling === spelling.toLowerCase() ? 1 : 0,
  })
  const ranked = entries.map(rank).sort(compareRankedSpellings)
  const pool = (fallback ? entries : plainSpellings)
    .map(rank)
    .sort(compareRankedSpellings)
  const best = pool[0].spelling
  const titleVariant = fallback
    ? [...pool[0].variants].sort(compareLeadCandidates)[0]
    : pool[0].variants
        .filter((variant) => isPlain(variant.parsed))
        .sort(
          (a, b) =>
            compareLines(a.parsed.line, b.parsed.line) ||
            codePoint(a.parsed.name, b.parsed.name)
        )[0]
  const inRankOrder = [
    pool[0],
    ...ranked.filter((entry) => entry.spelling !== best),
  ]
  const spellings = inRankOrder.map((entry) => entry.spelling)
  const plain = new Set(plainSpellings.map(([spelling]) => spelling))
  const inSpelling = (a: ModelVariant, b: ModelVariant) =>
    Number(isPlain(b.parsed)) - Number(isPlain(a.parsed)) ||
    (isPlain(a.parsed)
      ? compareLines(a.parsed.line, b.parsed.line)
      : compareLeadCandidates(a, b)) ||
    codePoint(a.parsed.name, b.parsed.name)
  const order = [titleVariant.parsed.name]
  for (const entry of inRankOrder) {
    for (const variant of [...entry.variants].sort(inSpelling)) {
      if (variant !== titleVariant) order.push(variant.parsed.name)
    }
  }
  return {
    title: fallback ? stripLineTag(titleVariant.parsed.name) : best,
    fullName: fallback,
    titleModel: titleVariant.parsed.name,
    alsoWrittenAs: spellings.filter(
      (spelling) => spelling !== best && plain.has(spelling)
    ),
    spellings,
    order,
  }
}

/** The first displayed member in the family's lead order. */
function pickLead(
  displayed: readonly ModelVariant[],
  title: FamilyTitle
): ModelVariant {
  const rank = new Map(title.order.map((name, index) => [name, index]))
  const rankOf = (variant: ModelVariant) =>
    rank.get(variant.parsed.name) ?? Number.POSITIVE_INFINITY
  let lead = displayed[0]
  for (const variant of displayed) {
    if (rankOf(variant) < rankOf(lead)) lead = variant
  }
  return lead
}

/**
 * The lead's vendor, else the most common vendor among the displayed members
 * (ties: the earliest member), with that member's icon.
 */
function familyVendor(
  members: readonly ModelVariant[],
  lead: ModelVariant
): ModelFamily['vendor'] {
  if (lead.model.vendor_name) {
    return { name: lead.model.vendor_name, icon: lead.model.vendor_icon }
  }
  const counts = new Map<string, number>()
  for (const { model } of members) {
    if (model.vendor_name) {
      counts.set(model.vendor_name, (counts.get(model.vendor_name) ?? 0) + 1)
    }
  }
  let best: PricingModel | undefined
  let bestCount = 0
  for (const { model } of members) {
    const count = model.vendor_name ? (counts.get(model.vendor_name) ?? 0) : 0
    if (count > bestCount) {
      best = model
      bestCount = count
    }
  }
  return best?.vendor_name
    ? { name: best.vendor_name, icon: best.vendor_icon }
    : undefined
}

/**
 * Group an already filtered and sorted model list into families. Families keep
 * the order in which their first member appears, so the active sort orders
 * families by their best member. Keys and titles come from the catalogue
 * index, so a card keeps its name while the list is filtered.
 */
export function groupModelFamilies(
  models: readonly PricingModel[],
  index: ModelSquareIndex
): ModelFamily[] {
  let fallbackOptions: ParseModelNameOptions | undefined
  const parse = (name: string) => {
    const parsed = index.byName.get(name)
    if (parsed) return parsed
    fallbackOptions ??= parseOptions(index.settings)
    return parseModelName(name, fallbackOptions)
  }
  const byKey = groupByKey(
    models.map((model) => ({ model, parsed: parse(model.model_name || '') }))
  )

  return [...byKey.entries()].map(([key, members]) => {
    const byLine = new Map<string, ModelVariant[]>()
    for (const member of members) {
      const lineMembers = byLine.get(member.parsed.line)
      if (lineMembers) lineMembers.push(member)
      else byLine.set(member.parsed.line, [member])
    }
    const lines = [...byLine.entries()]
      .sort(([a], [b]) => compareLines(a, b))
      .map(([line, variants]) => ({
        line,
        variants: [...variants].sort(compareVariants),
      }))
    const variants = lines.flatMap((line) => line.variants)
    const title = index.titles.get(key) ?? chooseFamilyTitle(members)
    const lead = pickLead(members, title)
    let variantLabels = [...variants]
      .sort(compareVariants)
      .map((variant) => variant.parsed.label)
      .filter((label, position, labels) => labels.indexOf(label) === position)
    if (title.fullName && lead.parsed.name === title.titleModel) {
      // A full-name title names one member: its chip comes first.
      variantLabels = [
        lead.parsed.label,
        ...variantLabels.filter((label) => label !== lead.parsed.label),
      ]
    }

    return {
      key,
      title: title.title,
      titleIsFullName: title.fullName,
      alsoWrittenAs: title.alsoWrittenAs,
      lead: lead.model,
      vendor: familyVendor(members, lead),
      models: members.map((member) => member.model),
      lines,
      variants,
      variantLabels,
    }
  })
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * Read variant rules from untrusted JSON (API payload or a stored option).
 * Returns null when the value is not an array of rule objects.
 */
export function parseVariantRules(value: unknown): ModelVariantRule[] | null {
  let raw = value
  if (typeof raw === 'string') {
    try {
      raw = JSON.parse(raw)
    } catch {
      return null
    }
  }
  if (!Array.isArray(raw)) return null
  const rules: ModelVariantRule[] = []
  for (const item of raw) {
    if (!isRecord(item)) return null
    if (typeof item.suffix !== 'string' || typeof item.label !== 'string') {
      return null
    }
    rules.push({
      suffix: item.suffix,
      label: item.label,
      description: typeof item.description === 'string' ? item.description : '',
    })
  }
  return rules
}

/**
 * Read the family base list from untrusted JSON (API payload or a stored
 * option). Returns null when the value is not an array of strings.
 */
export function parseFamilyBases(value: unknown): string[] | null {
  let raw = value
  if (typeof raw === 'string') {
    try {
      raw = JSON.parse(raw)
    } catch {
      return null
    }
  }
  if (!Array.isArray(raw)) return null
  if (!raw.every((item) => typeof item === 'string')) return null
  return raw as string[]
}

function booleanOr(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback
}

/** Each section shown or hidden as sent, its default when missing or malformed. */
function resolveFilterSections(value: unknown): ModelSquareFilterSections {
  const sections = isRecord(value) ? value : {}
  return {
    groups: booleanOr(sections.groups, DEFAULT_FILTER_SECTIONS.groups),
    vendors: booleanOr(sections.vendors, DEFAULT_FILTER_SECTIONS.vendors),
    tags: booleanOr(sections.tags, DEFAULT_FILTER_SECTIONS.tags),
    quota_types: booleanOr(
      sections.quota_types,
      DEFAULT_FILTER_SECTIONS.quota_types
    ),
    endpoint_types: booleanOr(
      sections.endpoint_types,
      DEFAULT_FILTER_SECTIONS.endpoint_types
    ),
  }
}

/**
 * Resolve the Model Square display settings from the pricing payload. Older
 * backends omit them, or some of them: grouping and both folds are then on
 * with the built-in rules and no family bases, and the filter sections take
 * their defaults (model tags and endpoint types hidden).
 */
export function resolveModelSquareSettings(
  value: unknown
): ModelSquareSettings {
  if (!isRecord(value)) {
    return {
      variant_grouping_enabled: true,
      version_spelling_fold_enabled: true,
      snapshot_fold_enabled: true,
      variant_rules: [...DEFAULT_MODEL_VARIANT_RULES],
      family_bases: [],
      filter_sections: { ...DEFAULT_FILTER_SECTIONS },
    }
  }
  // A Go nil slice encodes an empty rule list as null.
  const rules =
    value.variant_rules === null ? [] : parseVariantRules(value.variant_rules)
  return {
    variant_grouping_enabled: value.variant_grouping_enabled !== false,
    version_spelling_fold_enabled: booleanOr(
      value.version_spelling_fold_enabled,
      true
    ),
    snapshot_fold_enabled: booleanOr(value.snapshot_fold_enabled, true),
    variant_rules: (rules ?? [...DEFAULT_MODEL_VARIANT_RULES]).filter(
      (rule) => rule.suffix && rule.label
    ),
    family_bases: (parseFamilyBases(value.family_bases) ?? []).filter(Boolean),
    filter_sections: resolveFilterSections(value.filter_sections),
  }
}

/**
 * The characters Go's unicode.IsSpace accepts. JavaScript's `\s` differs: it
 * also matches U+FEFF and misses U+0085 (NEL).
 */
const GO_SPACE_PATTERN =
  /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/

/** Length in Unicode code points, like Go's utf8.RuneCountInString. */
function codePointLength(value: string): number {
  return [...value].length
}

/** Blank after Go's strings.TrimSpace. */
function isGoBlank(value: string): boolean {
  return [...value].every((char) => GO_SPACE_PATTERN.test(char))
}

/** Validate rules with the same constraints the backend enforces. */
export function validateVariantRules(
  rules: readonly ModelVariantRule[]
): VariantRuleIssue[] {
  const issues: VariantRuleIssue[] = []
  const limits = MODEL_VARIANT_RULE_LIMITS
  const seen = new Set<string>()
  rules.forEach((rule, index) => {
    const suffix = rule.suffix
    if (!suffix) {
      issues.push({ index, field: 'suffix', message: 'Suffix is required' })
    } else if (codePointLength(suffix) > limits.maxSuffixLength) {
      issues.push({
        index,
        field: 'suffix',
        message: 'Must be {{max}} characters or fewer',
        values: { max: limits.maxSuffixLength },
      })
    } else if (GO_SPACE_PATTERN.test(suffix)) {
      issues.push({
        index,
        field: 'suffix',
        message: 'Suffix must not contain spaces',
      })
    } else if (seen.has(suffix)) {
      issues.push({ index, field: 'suffix', message: 'Duplicate suffix' })
    }
    seen.add(suffix)

    if (isGoBlank(rule.label)) {
      issues.push({ index, field: 'label', message: 'Label is required' })
    } else if (codePointLength(rule.label) > limits.maxLabelLength) {
      issues.push({
        index,
        field: 'label',
        message: 'Must be {{max}} characters or fewer',
        values: { max: limits.maxLabelLength },
      })
    }

    if (codePointLength(rule.description ?? '') > limits.maxDescriptionLength) {
      issues.push({
        index,
        field: 'description',
        message: 'Must be {{max}} characters or fewer',
        values: { max: limits.maxDescriptionLength },
      })
    }
  })
  return issues
}

export type FamilyBaseIssue = {
  index: number
  /** i18n key */
  message: string
  values?: Record<string, number | string>
}

/**
 * One base per line, as typed in the settings: lines are trimmed, blank lines
 * dropped and repeats kept once.
 */
export function splitFamilyBases(text: string): string[] {
  const bases: string[] = []
  for (const line of text.split('\n')) {
    const base = line.trim()
    if (base && !bases.includes(base)) bases.push(base)
  }
  return bases
}

/** Validate family bases with the same constraints the backend enforces. */
export function validateFamilyBases(
  bases: readonly string[]
): FamilyBaseIssue[] {
  const issues: FamilyBaseIssue[] = []
  const limits = MODEL_FAMILY_BASE_LIMITS
  if (bases.length > limits.maxBases) {
    issues.push({
      index: -1,
      message: 'At most {{max}} base models',
      values: { max: limits.maxBases },
    })
  }
  bases.forEach((base, index) => {
    const values = { base }
    if (codePointLength(base) > limits.maxBaseLength) {
      issues.push({
        index,
        message: '{{base}}: must be {{max}} characters or fewer',
        values: { ...values, max: limits.maxBaseLength },
      })
    } else if (GO_SPACE_PATTERN.test(base)) {
      issues.push({
        index,
        message: '{{base}}: must not contain spaces',
        values,
      })
    } else if (base.startsWith('[')) {
      issues.push({
        index,
        message: '{{base}}: leave out the leading [...] line tag',
        values,
      })
    } else if (base.endsWith('-')) {
      issues.push({ index, message: '{{base}}: must not end with -', values })
    }
  })
  return issues
}
