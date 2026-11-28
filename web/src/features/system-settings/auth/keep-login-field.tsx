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
import type { TFunction } from 'i18next'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { FormControl, FormDescription, FormLabel } from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'

import {
  SettingsFormGridItem,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'

type KeepLoginNote = 'env' | 'saved-off' | 'saved-on'

/**
 * The note under the switch: SESSION_SECRET fixes the key, or the saved value
 * differs from what this run started with, so the next restart changes things.
 */
function keepLoginNote(source: string, saved: boolean): KeepLoginNote | null {
  if (source === 'env') return 'env'
  if (source === 'database' && !saved) return 'saved-off'
  if (source === 'random' && saved) return 'saved-on'
  return null
}

function keepLoginNoteText(t: TFunction, note: KeepLoginNote): string {
  switch (note) {
    case 'env':
      return t(
        'Fixed by the SESSION_SECRET environment variable; this switch has no effect.'
      )
    case 'saved-off':
      return t(
        'Saved as off. Users stay signed in until the next restart; that restart and every one after it sign everyone out.'
      )
    case 'saved-on':
      return t(
        'Saved as on, but this run did not start with the kept key, so the next restart still signs everyone out once; after that, users stay signed in. If every restart signs users out, check the server log.'
      )
  }
}

type KeepLoginFieldProps = {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  /** The saved value, which the server reads at its next start */
  saved: boolean
  /** Where this server run's login signing key came from: 'env' | 'database' | 'random', '' if unknown */
  source: string
}

/**
 * The "keep users signed in after restarts" switch and its note. Render it
 * in the switch's FormField. Saving changes nothing until the next restart,
 * so the note follows the saved value, not the unsaved one.
 */
export function KeepLoginField(props: KeepLoginFieldProps) {
  const { t } = useTranslation()
  const noteId = useId()
  const note = keepLoginNote(props.source, props.saved)

  return (
    <SettingsFormGridItem span='full'>
      <SettingsSwitchItem>
        <SettingsSwitchContent>
          <FormLabel>{t('Keep users signed in after restarts')}</FormLabel>
          <FormDescription>
            {t(
              'When on, signed-in users stay signed in when the server restarts or is updated: the key that signs logins is kept in the database and used again after a restart. A change takes effect at the next restart. When off, every restart signs everyone out and the kept key is deleted, so turning this on again later starts with a new key. While on, anyone who gets a copy of the database or a backup can impersonate any signed-in user, administrators included: protect your backups, or set SESSION_SECRET on the server instead. When SESSION_SECRET is set, it is used and this switch has no effect.'
            )}
          </FormDescription>
        </SettingsSwitchContent>
        <FormControl describedBy={note ? noteId : undefined}>
          <Switch
            checked={props.checked}
            onCheckedChange={props.onCheckedChange}
            disabled={props.source === 'env'}
          />
        </FormControl>
      </SettingsSwitchItem>
      {note ? (
        <Alert id={noteId} role='status'>
          <AlertDescription>{keepLoginNoteText(t, note)}</AlertDescription>
        </Alert>
      ) : null}
    </SettingsFormGridItem>
  )
}
