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
import { describe, expect, test } from 'vitest'

import {
  findStaticImports,
  staticImportClosure,
} from '@/test-utils/static-import-graph'

const CODE_EDITOR =
  /^(components\/ai-elements\/code-block\.tsx|package:@codemirror\/)/
const DIALOGS_AND_DRAWERS =
  /^features\/channels\/components\/(dialogs|drawers)\//

describe('channel list chunks', () => {
  test('the channel list loads no dialog, drawer or code editor until one opens', () => {
    const closure = staticImportClosure(
      'routes/_authenticated/channels/index.tsx'
    )

    expect(findStaticImports(closure, CODE_EDITOR)).toEqual([])
    expect(findStaticImports(closure, DIALOGS_AND_DRAWERS)).toEqual([])
  })
})
