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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import {
  GROUP_ACCESS_MAX_MIN_TOPUP,
  formatGroupAccessUser,
  formatGroupAccessUsersText,
  groupAccessRuleFormSchema,
  parseGroupAccessUsers,
  type GroupAccessRule,
  type GroupAccessRuleFormValues,
} from './group-access-rules'

const GROUP_ACCESS_RULE_FORM_ID = 'group-access-rule-form'

type GroupAccessRuleDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The rule being edited, or null when adding one. */
  rule: GroupAccessRule | null
  /** Groups that can be chosen: unruled groups plus the edited rule's group. */
  groupOptions: string[]
  onSave: (rule: GroupAccessRule) => void
}

export function GroupAccessRuleDialog(props: GroupAccessRuleDialogProps) {
  const { t } = useTranslation()
  const isEditMode = props.rule !== null

  const form = useForm<GroupAccessRuleFormValues>({
    resolver: zodResolver(groupAccessRuleFormSchema),
    defaultValues: { group: '', minTopup: 0, users: '' },
  })

  useEffect(() => {
    if (!props.open) return
    form.reset({
      group: props.rule?.group ?? '',
      minTopup: props.rule?.min_topup ?? 0,
      users: formatGroupAccessUsersText(props.rule?.users ?? []),
    })
  }, [form, props.open, props.rule])

  const usersText = form.watch('users')
  const parsedUsers = useMemo(
    () => parseGroupAccessUsers(usersText ?? ''),
    [usersText]
  )
  const knownUsernames = useMemo(
    () => new Map((props.rule?.users ?? []).map((u) => [u.id, u.username])),
    [props.rule]
  )
  // Keep the last known username of an ID until the server refreshes it.
  const previewUsers = useMemo(
    () =>
      parsedUsers.users.map((user) =>
        user.id > 0
          ? { id: user.id, username: knownUsernames.get(user.id) ?? '' }
          : user
      ),
    [parsedUsers, knownUsernames]
  )
  const groupComboboxOptions = useMemo(
    () => props.groupOptions.map((group) => ({ value: group, label: group })),
    [props.groupOptions]
  )

  const handleSubmit = (values: GroupAccessRuleFormValues) => {
    props.onSave({
      group: values.group,
      min_topup: values.minTopup,
      users: previewUsers,
    })
    props.onOpenChange(false)
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        isEditMode ? t('Edit group access rule') : t('Add group access rule')
      }
      description={t(
        'Users qualify by reaching the cumulative top-up threshold or by being on the whitelist.'
      )}
      contentClassName='sm:max-w-[560px]'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={GROUP_ACCESS_RULE_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={GROUP_ACCESS_RULE_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='group'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Group')}</FormLabel>
                <FormControl>
                  <Combobox
                    options={groupComboboxOptions}
                    value={field.value}
                    onValueChange={(value) => field.onChange(value ?? '')}
                    onBlur={field.onBlur}
                    name={field.name}
                    ref={field.ref}
                    placeholder={t('Select a group')}
                    emptyText={t('No group found.')}
                    className='w-full'
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='minTopup'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Minimum cumulative top-up')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    inputMode='decimal'
                    min={0}
                    max={GROUP_ACCESS_MAX_MIN_TOPUP}
                    step='any'
                    name={field.name}
                    ref={field.ref}
                    onBlur={field.onBlur}
                    value={Number.isNaN(field.value) ? '' : field.value}
                    onChange={(event) =>
                      field.onChange(event.target.valueAsNumber)
                    }
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Same unit as top-up amounts. 0 means only whitelisted users can use the group.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='users'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Whitelisted users')}</FormLabel>
                <FormControl>
                  <Textarea
                    {...field}
                    rows={4}
                    placeholder={t('One per line, e.g. 12 or alice')}
                    className='font-mono'
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Separate entries with new lines, commas or spaces. Digits are a user ID, anything else a username; prefix @ to force a username, e.g. @123.'
                  )}
                </FormDescription>
                <FormMessage />
                <div className='space-y-2'>
                  <p
                    className='text-muted-foreground text-xs'
                    aria-live='polite'
                  >
                    {t('Whitelisted users: {{count}}', {
                      count: previewUsers.length,
                    })}
                  </p>
                  {previewUsers.length > 0 && (
                    <ul
                      aria-label={t('Parsed users preview')}
                      className='flex max-h-24 flex-wrap gap-1 overflow-y-auto'
                    >
                      {previewUsers.map((user) => (
                        <li key={formatGroupAccessUser(user)}>
                          <Badge variant='outline' className='font-mono'>
                            {formatGroupAccessUser(user)}
                          </Badge>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
