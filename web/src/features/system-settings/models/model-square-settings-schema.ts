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
import * as z from 'zod'

import {
  DEFAULT_MODEL_VARIANT_RULES,
  MODEL_VARIANT_RULE_LIMITS,
  parseVariantRules,
  validateVariantRules,
} from '@/features/pricing/lib/model-variants'

export const modelSquareSettingsSchema = z.object({
  variant_grouping_enabled: z.boolean(),
  variant_rules: z
    .array(
      z.object({
        suffix: z.string(),
        label: z.string(),
        description: z.string(),
      })
    )
    .superRefine((rules, ctx) => {
      if (rules.length > MODEL_VARIANT_RULE_LIMITS.maxRules) {
        ctx.addIssue({
          code: 'custom',
          message: 'At most {{max}} rules',
        })
      }
      for (const issue of validateVariantRules(rules)) {
        ctx.addIssue({
          code: 'custom',
          path: [issue.index, issue.field],
          message: issue.message,
        })
      }
    }),
})

export type ModelSquareSettingsFormValues = z.output<
  typeof modelSquareSettingsSchema
>

/** Stored option value of a rule list, as the backend expects it. */
export function serializeVariantRules(
  rules: ModelSquareSettingsFormValues['variant_rules']
): string {
  return JSON.stringify(
    rules.map((rule) => ({
      suffix: rule.suffix,
      label: rule.label,
      description: rule.description,
    }))
  )
}

/** Form rows for a stored option value; empty or invalid uses the defaults. */
export function toVariantRuleFormValues(
  value: string
): ModelSquareSettingsFormValues['variant_rules'] {
  const rules = value.trim()
    ? (parseVariantRules(value) ?? DEFAULT_MODEL_VARIANT_RULES)
    : DEFAULT_MODEL_VARIANT_RULES
  return rules.map((rule) => ({
    suffix: rule.suffix,
    label: rule.label,
    description: rule.description ?? '',
  }))
}
