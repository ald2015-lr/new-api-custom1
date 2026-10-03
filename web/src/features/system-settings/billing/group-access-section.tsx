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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Info, Plus } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormLabel,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'
import { getGroups } from '@/features/users/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getSystemOptions } from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import { GroupAccessRuleDialog } from './group-access-rule-dialog'
import {
  GROUP_ACCESS_COUNT_REDEMPTION_KEY,
  GROUP_ACCESS_RULES_KEY,
  parseGroupAccessRules,
  serializeGroupAccessRules,
  type GroupAccessRule,
} from './group-access-rules'
import { GroupAccessRulesTable } from './group-access-rules-table'

type GroupAccessFormValues = {
  countRedemption: boolean
  rules: GroupAccessRule[]
}

type GroupAccessSectionProps = {
  defaultValues: {
    /** Stored JSON string of `group_access_setting.rules`. */
    rules: string
    countRedemption: boolean
  }
}

export function GroupAccessSection(props: GroupAccessSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const queryClient = useQueryClient()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingRule, setEditingRule] = useState<GroupAccessRule | null>(null)
  const [deletingGroup, setDeletingGroup] = useState<string | null>(null)

  const formDefaults = useMemo<GroupAccessFormValues>(
    () => ({
      countRedemption: props.defaultValues.countRedemption,
      rules: parseGroupAccessRules(props.defaultValues.rules),
    }),
    [props.defaultValues.countRedemption, props.defaultValues.rules]
  )

  const form = useForm<GroupAccessFormValues>({ defaultValues: formDefaults })
  // Picks up option changes made elsewhere. After a save, onSubmit resets the
  // form from the refetched options itself, because the normalized value can
  // equal the stored one and then these defaults do not change.
  useResetForm(form, formDefaults)

  const { isDirty, isSubmitting } = form.formState
  const rules = form.watch('rules')

  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
    staleTime: 5 * 60 * 1000,
  })

  const dialogGroupOptions = useMemo(() => {
    const ruledGroups = new Set(rules.map((rule) => rule.group))
    const options = new Set(
      (groupsData?.data ?? []).filter(
        (group) => group !== 'auto' && !ruledGroups.has(group)
      )
    )
    if (editingRule) options.add(editingRule.group)
    return [...options].sort((a, b) => a.localeCompare(b))
  }, [groupsData, rules, editingRule])

  const setRules = (nextRules: GroupAccessRule[]) => {
    form.setValue('rules', nextRules, { shouldDirty: true })
  }

  const handleSaveRule = (rule: GroupAccessRule) => {
    if (!editingRule) {
      setRules([...rules, rule])
      return
    }
    setRules(
      rules.map((current) =>
        current.group === editingRule.group ? rule : current
      )
    )
  }

  const handleConfirmDelete = () => {
    setRules(rules.filter((rule) => rule.group !== deletingGroup))
    setDeletingGroup(null)
  }

  // The server resolves usernames and IDs and sorts and de-duplicates users,
  // so show what it stored rather than what was submitted.
  const loadSavedValues = async (
    submitted: GroupAccessFormValues
  ): Promise<GroupAccessFormValues> => {
    try {
      const response = await queryClient.fetchQuery({
        queryKey: ['system-options'],
        queryFn: async () => requireServerSuccess(await getSystemOptions()),
        staleTime: 0,
      })
      const options = new Map(
        (response.data ?? []).map((option) => [option.key, option.value])
      )
      const rulesValue = options.get(GROUP_ACCESS_RULES_KEY)
      const countValue = options.get(GROUP_ACCESS_COUNT_REDEMPTION_KEY)
      return {
        rules:
          rulesValue === undefined
            ? submitted.rules
            : parseGroupAccessRules(rulesValue),
        countRedemption:
          countValue === undefined
            ? submitted.countRedemption
            : countValue === 'true' || countValue === '1',
      }
    } catch {
      // The save itself succeeded; fall back to the submitted values.
      return submitted
    }
  }

  const onSubmit = async (values: GroupAccessFormValues) => {
    const updates: Array<{ key: string; value: string }> = []
    const rulesValue = serializeGroupAccessRules(values.rules)
    if (rulesValue !== serializeGroupAccessRules(formDefaults.rules)) {
      updates.push({ key: GROUP_ACCESS_RULES_KEY, value: rulesValue })
    }
    if (values.countRedemption !== formDefaults.countRedemption) {
      updates.push({
        key: GROUP_ACCESS_COUNT_REDEMPTION_KEY,
        value: String(values.countRedemption),
      })
    }
    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    try {
      for (const update of updates) {
        await updateOption.mutateAsync(update)
      }
    } catch {
      // useUpdateOption already shows the server's reason; keep the edits.
      return
    }
    form.reset(await loadSavedValues(values))
  }

  return (
    <SettingsSection title={t('Group Access')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save group access'
          />

          <Alert role='note'>
            <Info aria-hidden='true' />
            <AlertDescription>
              <ul className='list-disc space-y-1 pl-4'>
                <li>
                  {t(
                    'A gated group can be used only by users whose cumulative top-up reaches its threshold, in the same unit as top-up amounts, or by whitelisted users.'
                  )}
                </li>
                <li>
                  {t(
                    'Administrators and users whose own group is the gated group are always allowed. A threshold of 0 makes the group whitelist-only.'
                  )}
                </li>
                <li>
                  {t(
                    'Existing API keys bound to a gated group stop working for users who do not qualify.'
                  )}
                </li>
              </ul>
            </AlertDescription>
          </Alert>

          <FormField
            control={form.control}
            name='countRedemption'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Count redemption codes as top-up')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Include redeemed codes in the cumulative top-up checked against thresholds.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={updateOption.isPending || isSubmitting}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <div data-settings-form-span='full' className='min-w-0 space-y-3'>
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <h4 className='text-sm font-medium'>{t('Rules')}</h4>
              <Button
                type='button'
                size='sm'
                onClick={() => {
                  setEditingRule(null)
                  setDialogOpen(true)
                }}
              >
                <Plus data-icon='inline-start' />
                {t('Add rule')}
              </Button>
            </div>

            <GroupAccessRulesTable
              rules={rules}
              onEdit={(rule) => {
                setEditingRule(rule)
                setDialogOpen(true)
              }}
              onDelete={(rule) => setDeletingGroup(rule.group)}
            />
          </div>
        </SettingsForm>
      </Form>

      <GroupAccessRuleDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        rule={editingRule}
        groupOptions={dialogGroupOptions}
        onSave={handleSaveRule}
      />

      <ConfirmDialog
        open={deletingGroup !== null}
        onOpenChange={(open) => {
          if (!open) setDeletingGroup(null)
        }}
        title={t('Delete rule')}
        desc={t(
          'Remove the access rule for group {{group}}? The change applies after you save.',
          { group: deletingGroup ?? '' }
        )}
        confirmText={t('Delete')}
        destructive
        handleConfirm={handleConfirmDelete}
      />
    </SettingsSection>
  )
}
