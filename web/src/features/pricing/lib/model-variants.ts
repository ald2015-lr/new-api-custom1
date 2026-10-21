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

// ----------------------------------------------------------------------------
// Model variant grouping (display only: model names never change)
// ----------------------------------------------------------------------------

export type ModelVariantRule = {
  suffix: string
  label: string
  description?: string
}

export type ModelSquareSettings = {
  variant_grouping_enabled: boolean
  variant_rules: ModelVariantRule[]
  /**
   * Base models named by the admin: every `<base>-<anything>` model joins the
   * base's family, even when no rule covers its suffix.
   */
  family_bases: string[]
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

const LINE_TAG_PATTERN = /^\[([^\]]+)\]/
/** Lines listed first, in this order; other lines follow alphabetically. */
const PREFERRED_LINE_ORDER = ['按次', '按量']

export type ParsedModelName = {
  name: string
  /** Text inside the leading `[...]` tag, or '' when the name has none. */
  line: string
  /** Family key: the name without the line tag and the stripped suffixes. */
  base: string
  /** Stripped suffixes in name order (left to right). */
  suffixes: string[]
  /** Rule index of each stripped suffix, in name order. */
  ruleIndexes: number[]
  /**
   * The rest of the name after a configured family base and before any rule
   * suffix (`low` in `gemini-4-argon-low`); '' otherwise.
   */
  extra: string
  /** Labels joined with ` · `; '' for the standard (no suffix) variant. */
  label: string
  /** Non-empty rule descriptions joined with `；`. */
  description: string
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
  base: string
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
  const tag = LINE_TAG_PATTERN.exec(name)
  return tag ? name.slice(tag[0].length) : name
}

/**
 * The longest configured base the name is, or starts with followed by `-`.
 * Bases never end with `-`, so only the whole name and its prefixes that stop
 * right before a `-` can match: a few set lookups per name, however many
 * bases there are.
 */
function matchFamilyBase(
  name: string,
  bases: ReadonlySet<string>
): string | undefined {
  if (bases.size === 0) return undefined
  if (bases.has(name)) return name
  for (
    let dash = name.lastIndexOf('-');
    dash > 0;
    dash = name.lastIndexOf('-', dash - 1)
  ) {
    const prefix = name.slice(0, dash)
    if (bases.has(prefix)) return prefix
  }
  return undefined
}

function toBaseSet(
  familyBases: readonly string[] | ReadonlySet<string>
): ReadonlySet<string> {
  return familyBases instanceof Set ? familyBases : new Set(familyBases)
}

/**
 * Parse a model name into its line, base model and variant suffixes.
 *
 * Suffixes are stripped from the end, longest matching rule first, until no
 * rule matches or stripping would leave an empty base or one ending in `-`.
 * When the name (without its line tag) starts with a configured family base,
 * that base is the family and rules never cut into it; whatever is left
 * between the base and the stripped suffixes names the variant (`low`).
 */
export function parseModelName(
  name: string,
  rules: readonly ModelVariantRule[],
  familyBases: readonly string[] | ReadonlySet<string> = []
): ParsedModelName {
  const tag = LINE_TAG_PATTERN.exec(name)
  const line = tag ? tag[1] : ''
  let base = tag ? name.slice(tag[0].length) : name
  const familyBase = matchFamilyBase(base, toBaseSet(familyBases))
  const minLength = familyBase?.length ?? 0
  const stripped: { suffix: string; index: number }[] = []

  for (;;) {
    let match = -1
    for (let index = 0; index < rules.length; index++) {
      const suffix = rules[index].suffix
      if (
        suffix &&
        base.endsWith(suffix) &&
        base.length - suffix.length >= minLength &&
        (match < 0 || suffix.length > rules[match].suffix.length)
      ) {
        match = index
      }
    }
    if (match < 0) break
    const rest = base.slice(0, base.length - rules[match].suffix.length)
    if (!rest || rest.endsWith('-')) break
    stripped.unshift({ suffix: rules[match].suffix, index: match })
    base = rest
  }

  let extra = ''
  if (familyBase !== undefined) {
    extra = base.slice(familyBase.length + 1)
    base = familyBase
  }
  const labels = stripped.map((item) => rules[item.index].label)
  if (extra) labels.unshift(extra)

  return {
    name,
    line,
    base,
    suffixes: stripped.map((item) => item.suffix),
    ruleIndexes: stripped.map((item) => item.index),
    extra,
    label: labels.join(' · '),
    description: stripped
      .map((item) => rules[item.index].description?.trim() ?? '')
      .filter(Boolean)
      .join('；'),
  }
}

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
 * the rest alphabetically.
 */
function extraRank(extra: string): number {
  if (!extra) return -1
  const rank = EFFORT_ORDER.indexOf(extra)
  return rank < 0 ? EFFORT_ORDER.length : rank
}

function compareVariants(a: ModelVariant, b: ModelVariant): number {
  return (
    extraRank(a.parsed.extra) - extraRank(b.parsed.extra) ||
    a.parsed.extra.localeCompare(b.parsed.extra) ||
    compareRuleIndexes(a.parsed.ruleIndexes, b.parsed.ruleIndexes) ||
    a.parsed.name.localeCompare(b.parsed.name)
  )
}

function lineRank(line: string): number {
  if (!line) return -1
  const preferred = PREFERRED_LINE_ORDER.indexOf(line)
  return preferred < 0 ? PREFERRED_LINE_ORDER.length : preferred
}

export function compareLines(a: string, b: string): number {
  return lineRank(a) - lineRank(b) || a.localeCompare(b)
}

/**
 * Group an already filtered and sorted model list into families. Families keep
 * the order in which their first member appears, so the active sort orders
 * families by their best member.
 */
export function groupModelFamilies(
  models: readonly PricingModel[],
  rules: readonly ModelVariantRule[],
  familyBases: readonly string[] = []
): ModelFamily[] {
  const families = new Map<string, ModelVariant[]>()
  const bases = new Set(familyBases)
  for (const model of models) {
    const parsed = parseModelName(model.model_name || '', rules, bases)
    const members = families.get(parsed.base)
    if (members) members.push({ model, parsed })
    else families.set(parsed.base, [{ model, parsed }])
  }

  return [...families.entries()].map(([key, members]) => {
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
    const variantLabels = [...variants]
      .sort(compareVariants)
      .map((variant) => variant.parsed.label)
      .filter((label, index, labels) => labels.indexOf(label) === index)

    return {
      key,
      base: key,
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

/**
 * Resolve the Model Square display settings from the pricing payload. Older
 * backends omit them: grouping is then on with the built-in rules and no
 * family bases.
 */
export function resolveModelSquareSettings(
  value: unknown
): ModelSquareSettings {
  if (!isRecord(value)) {
    return {
      variant_grouping_enabled: true,
      variant_rules: [...DEFAULT_MODEL_VARIANT_RULES],
      family_bases: [],
    }
  }
  // A Go nil slice encodes an empty rule list as null.
  const rules =
    value.variant_rules === null ? [] : parseVariantRules(value.variant_rules)
  return {
    variant_grouping_enabled: value.variant_grouping_enabled !== false,
    variant_rules: (rules ?? [...DEFAULT_MODEL_VARIANT_RULES]).filter(
      (rule) => rule.suffix && rule.label
    ),
    family_bases: (parseFamilyBases(value.family_bases) ?? []).filter(Boolean),
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
