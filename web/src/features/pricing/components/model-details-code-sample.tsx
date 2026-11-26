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
import type { BundledLanguage } from 'shiki/bundle/web'

import {
  CodeBlock,
  CodeBlockCopyButton,
} from '@/components/ai-elements/code-block'

/**
 * A highlighted code sample with its copy button. Its own chunk, together
 * with the editor behind the highlighting (see model-details-lazy.tsx).
 */
export function ModelDetailsCodeSample(props: {
  code: string
  language: BundledLanguage
}) {
  return (
    <CodeBlock code={props.code} language={props.language}>
      <CodeBlockCopyButton />
    </CodeBlock>
  )
}
