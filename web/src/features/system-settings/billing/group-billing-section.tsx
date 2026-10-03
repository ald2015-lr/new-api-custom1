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
import { useQuery } from '@tanstack/react-query'
import { Info } from 'lucide-react'
import { useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Form } from '@/components/ui/form'
import { getGroups } from '@/features/users/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  FREE_GROUPS_KEY,
  buildGroupBillingRows,
  normalizeFreeGroups,
  parseFreeGroups,
  serializeFreeGroups,
} from './free-groups'
import { GroupBillingTable } from './group-billing-table'

type GroupBillingFormValues = {
  /** Sorted names of the groups that do not charge users. */
  freeGroups: string[]
}

type GroupBillingSectionProps = {
  defaultValues: {
    /** Stored JSON string of `group_billing_setting.free_groups`. */
    freeGroups: string
  }
}

export function GroupBillingSection(props: GroupBillingSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const formDefaults = useMemo<GroupBillingFormValues>(
    () => ({ freeGroups: parseFreeGroups(props.defaultValues.freeGroups) }),
    [props.defaultValues.freeGroups]
  )

  const form = useForm<GroupBillingFormValues>({ defaultValues: formDefaults })
  useResetForm(form, formDefaults)

  const { isDirty, isSubmitting } = form.formState
  const freeGroups = form.watch('freeGroups')
  const isSaving = updateOption.isPending || isSubmitting

  const groupsQuery = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
    staleTime: 5 * 60 * 1000,
  })

  const rows = useMemo(
    () => buildGroupBillingRows(groupsQuery.data?.data ?? [], freeGroups),
    [groupsQuery.data, freeGroups]
  )

  const handleChargeChange = (group: string, charge: boolean) => {
    const next = charge
      ? freeGroups.filter((name) => name !== group)
      : [...freeGroups, group]
    // Kept sorted so switching a group back leaves the form clean.
    form.setValue('freeGroups', normalizeFreeGroups(next), {
      shouldDirty: true,
    })
  }

  const onSubmit = async (values: GroupBillingFormValues) => {
    try {
      await updateOption.mutateAsync({
        key: FREE_GROUPS_KEY,
        value: serializeFreeGroups(values.freeGroups),
      })
    } catch {
      // useUpdateOption already shows the server's reason; keep the edits.
      return
    }
    form.reset({ freeGroups: normalizeFreeGroups(values.freeGroups) })
  }

  return (
    <SettingsSection title={t('Group Billing')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={isSaving}
            isSaveDisabled={!isDirty}
            saveLabel='Save group billing'
          />

          <Alert role='note' data-settings-form-span='full'>
            <Info aria-hidden='true' />
            <AlertDescription>
              <ul className='list-disc space-y-1 pl-4'>
                <li>
                  {t(
                    "Requests in a free group do not deduct the user's balance, subscription or API key quota."
                  )}
                </li>
                <li>
                  {t(
                    'Channel usage, channel quota limits and usage logs still count the real cost. Midjourney requests are still charged.'
                  )}
                </li>
                <li>
                  {t(
                    'Tip: combine a free group with a channel quota limit that resets daily to cap a daily free allowance.'
                  )}
                </li>
              </ul>
            </AlertDescription>
          </Alert>

          <div data-settings-form-span='full' className='min-w-0'>
            <GroupBillingTable
              rows={rows}
              isLoading={groupsQuery.isLoading}
              disabled={isSaving}
              onChargeChange={handleChargeChange}
            />
          </div>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
