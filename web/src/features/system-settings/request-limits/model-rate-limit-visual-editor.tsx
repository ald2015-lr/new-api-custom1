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
import { Plus, Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { ModelRateLimitDialog } from './model-rate-limit-dialog'
import {
  parseModelRateLimitRules,
  removeModelRateLimitRule,
  upsertModelRateLimitRule,
  type ModelRateLimitRule,
} from './model-rate-limit-rules'

type ModelRateLimitVisualEditorProps = {
  value: string
  onChange: (value: string) => void
}

export function ModelRateLimitVisualEditor(
  props: ModelRateLimitVisualEditorProps
) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [searchText, setSearchText] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editData, setEditData] = useState<ModelRateLimitRule | null>(null)

  // null when the backend could not read the rules either: they are left to
  // JSON mode, so adding a model cannot replace the ones the table cannot show.
  const parsedRules = useMemo(
    () => parseModelRateLimitRules(props.value),
    [props.value]
  )
  const rules = useMemo(() => parsedRules ?? [], [parsedRules])

  const filteredRules = useMemo(() => {
    if (!searchText) return rules
    const lowerSearch = searchText.toLowerCase()
    return rules.filter((rule) =>
      rule.modelName.toLowerCase().includes(lowerSearch)
    )
  }, [rules, searchText])

  let emptyContent = t(
    'No model-based rate limits configured. Click "Add model" to get started.'
  )
  if (parsedRules === null) {
    emptyContent = t(
      'These rules cannot be shown in visual mode. Fix them in JSON mode.'
    )
  } else if (searchText) {
    emptyContent = t('No models match your search')
  }

  const formatCount = (count: number) =>
    count === 0 ? t('Unlimited') : formatNumber(count, locale)

  const handleEdit = (rule: ModelRateLimitRule) => {
    setEditData(rule)
    setDialogOpen(true)
  }

  const handleAdd = () => {
    setEditData(null)
    setDialogOpen(true)
  }

  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex items-center gap-4'>
        <div className='relative flex-1'>
          <Search className='text-muted-foreground absolute top-2.5 left-2.5 h-4 w-4' />
          <Input
            placeholder={t('Search model names...')}
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            className='pl-9'
          />
        </div>
        <Button
          type='button'
          onClick={handleAdd}
          disabled={parsedRules === null}
        >
          <Plus className='mr-2 h-4 w-4' />
          {t('Add model')}
        </Button>
      </div>

      <StaticDataTable
        data={filteredRules}
        getRowKey={(rule) => rule.modelName}
        emptyContent={emptyContent}
        columns={[
          {
            id: 'model',
            header: t('Model Name'),
            cellClassName: 'font-medium',
            cell: (rule) => rule.modelName,
          },
          {
            id: 'duration',
            header: t('Time window (minutes)'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (rule) => (
              <span className='font-mono'>
                {formatNumber(rule.durationMinutes, locale)}
              </span>
            ),
          },
          {
            id: 'max-requests',
            header: t('Max Requests (incl. failures)'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (rule) => (
              <span className='font-mono'>{formatCount(rule.totalCount)}</span>
            ),
          },
          {
            id: 'max-success',
            header: t('Max Success'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (rule) => (
              <span className='font-mono'>
                {formatCount(rule.successCount)}
              </span>
            ),
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (rule) => (
              <StaticRowActions
                editLabel={t('Edit')}
                deleteLabel={t('Delete')}
                menuLabel={t('Open menu')}
                onEdit={() => handleEdit(rule)}
                onDelete={() =>
                  props.onChange(
                    removeModelRateLimitRule(props.value, rule.modelName)
                  )
                }
              />
            ),
          },
        ]}
      />

      <ModelRateLimitDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSave={(rule) =>
          props.onChange(upsertModelRateLimitRule(props.value, rule))
        }
        editData={editData}
        existingModelNames={rules.map((rule) => rule.modelName)}
      />
    </div>
  )
}
