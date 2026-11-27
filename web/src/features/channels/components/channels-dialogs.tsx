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
import { LazyDialog } from '@/components/lazy-dialog'
import { lazyWithRetry } from '@/lib/lazy-with-retry'

import { CHANNEL_TYPE_VLLM, CHANNEL_TYPE_SGLANG } from '../constants'
import { loadChannelMutateDrawer } from '../lib/channel-dialog-chunks'
import { useChannels } from './channels-provider'

// Every dialog and drawer is a chunk of its own, loaded when it first opens
// (see LazyDialog; after a failed load, the next opening loads it again), so
// the channel list does not download them, nor the code editor of the
// balance query, up front.
const ChannelMutateDrawer = lazyWithRetry(() =>
  loadChannelMutateDrawer().then((module) => ({
    default: module.ChannelMutateDrawer,
  }))
)
const BalanceQueryDialog = lazyWithRetry(() =>
  import('./dialogs/balance-query-dialog').then((module) => ({
    default: module.BalanceQueryDialog,
  }))
)
const ChannelQuotaLimitDialog = lazyWithRetry(() =>
  import('./dialogs/channel-quota-limit-dialog').then((module) => ({
    default: module.ChannelQuotaLimitDialog,
  }))
)
const ChannelTestDialog = lazyWithRetry(() =>
  import('./dialogs/channel-test-dialog').then((module) => ({
    default: module.ChannelTestDialog,
  }))
)
const CopyChannelDialog = lazyWithRetry(() =>
  import('./dialogs/copy-channel-dialog').then((module) => ({
    default: module.CopyChannelDialog,
  }))
)
const EditTagDialog = lazyWithRetry(() =>
  import('./dialogs/edit-tag-dialog').then((module) => ({
    default: module.EditTagDialog,
  }))
)
const FetchModelsDialog = lazyWithRetry(() =>
  import('./dialogs/fetch-models-dialog').then((module) => ({
    default: module.FetchModelsDialog,
  }))
)
const InferenceStatusDialog = lazyWithRetry(() =>
  import('./dialogs/inference-status-dialog').then((module) => ({
    default: module.InferenceStatusDialog,
  }))
)
const MultiKeyManageDialog = lazyWithRetry(() =>
  import('./dialogs/multi-key-manage-dialog').then((module) => ({
    default: module.MultiKeyManageDialog,
  }))
)
const OllamaModelsDialog = lazyWithRetry(() =>
  import('./dialogs/ollama-models-dialog').then((module) => ({
    default: module.OllamaModelsDialog,
  }))
)
const TagBatchEditDialog = lazyWithRetry(() =>
  import('./dialogs/tag-batch-edit-dialog').then((module) => ({
    default: module.TagBatchEditDialog,
  }))
)
const UpstreamUpdateDialog = lazyWithRetry(() =>
  import('./dialogs/upstream-update-dialog').then((module) => ({
    default: module.UpstreamUpdateDialog,
  }))
)

export function ChannelsDialogs() {
  const { open, setOpen, currentRow, upstream } = useChannels()
  const close = () => setOpen(null)
  const showInferenceStatus =
    open === 'inference-status' &&
    currentRow !== null &&
    [CHANNEL_TYPE_VLLM, CHANNEL_TYPE_SGLANG].includes(currentRow.type)
  const showQuotaLimit = open === 'quota-limit' && currentRow !== null

  return (
    <>
      {showInferenceStatus && (
        <LazyDialog open onLoadError={close}>
          <InferenceStatusDialog
            key={currentRow.id}
            provider={
              currentRow.type === CHANNEL_TYPE_SGLANG ? 'sglang' : 'vllm'
            }
            channelId={currentRow.id}
            channelName={currentRow.name}
            onClose={close}
            onSyncModels={() => setOpen('fetch-models')}
            onTestChannel={() => setOpen('test-channel')}
          />
        </LazyDialog>
      )}
      {showQuotaLimit && (
        <LazyDialog open onLoadError={close}>
          <ChannelQuotaLimitDialog
            key={currentRow.id}
            channel={currentRow}
            onOpenChange={(v) => !v && close()}
          />
        </LazyDialog>
      )}
      {/* Channel Create/Update Drawer */}
      <LazyDialog
        open={open === 'create-channel' || open === 'update-channel'}
        onLoadError={close}
      >
        <ChannelMutateDrawer
          open={open === 'create-channel' || open === 'update-channel'}
          onOpenChange={(v) => !v && close()}
          currentRow={open === 'update-channel' ? currentRow : null}
        />
      </LazyDialog>

      {/* Test Channel Dialog */}
      <LazyDialog open={open === 'test-channel'} onLoadError={close}>
        <ChannelTestDialog
          open={open === 'test-channel'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Balance Query Dialog */}
      <LazyDialog open={open === 'balance-query'} onLoadError={close}>
        <BalanceQueryDialog
          open={open === 'balance-query'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Fetch Models Dialog */}
      <LazyDialog open={open === 'fetch-models'} onLoadError={close}>
        <FetchModelsDialog
          open={open === 'fetch-models'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Ollama Models Dialog */}
      <LazyDialog open={open === 'ollama-models'} onLoadError={close}>
        <OllamaModelsDialog
          open={open === 'ollama-models'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Copy Channel Dialog */}
      <LazyDialog open={open === 'copy-channel'} onLoadError={close}>
        <CopyChannelDialog
          open={open === 'copy-channel'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Multi-Key Management Dialog */}
      <LazyDialog open={open === 'multi-key-manage'} onLoadError={close}>
        <MultiKeyManageDialog
          open={open === 'multi-key-manage'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Tag Batch Edit Dialog */}
      <LazyDialog open={open === 'tag-batch-edit'} onLoadError={close}>
        <TagBatchEditDialog
          open={open === 'tag-batch-edit'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Edit Tag Dialog */}
      <LazyDialog open={open === 'edit-tag'} onLoadError={close}>
        <EditTagDialog
          open={open === 'edit-tag'}
          onOpenChange={(v) => !v && close()}
        />
      </LazyDialog>

      {/* Upstream Model Update Dialog */}
      <LazyDialog
        open={upstream.showModal && Boolean(upstream.channel)}
        onLoadError={upstream.closeModal}
      >
        <UpstreamUpdateDialog upstream={upstream} />
      </LazyDialog>
    </>
  )
}
