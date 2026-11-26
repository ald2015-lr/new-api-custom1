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

/**
 * Id and title of a settings section, without its component.
 */
export type SectionMetaDefinition = {
  id: string
  titleKey: string
}

/**
 * Section metadata configuration
 */
export type SectionMetaConfig<TSectionId extends string> = {
  sections: readonly SectionMetaDefinition[]
  defaultSection: TSectionId
  basePath: string
  /** 'query' = `${basePath}?section=${id}`, 'path' = `${basePath}/${id}` */
  urlStyle?: 'query' | 'path'
}

/**
 * Section ids and sidebar items of a settings page, without its components.
 *
 * Route guards and the sidebar only need these, so each settings page keeps
 * them in a `section-meta.ts` next to its `section-registry.tsx`. Imported
 * from there, the section components stay in the page's own lazy chunk
 * instead of the entry or the layout chunk.
 */
export function createSectionMeta<TSectionId extends string>(
  config: SectionMetaConfig<TSectionId>
) {
  const urlStyle = config.urlStyle ?? 'query'

  const sectionIds = config.sections.map((section) => section.id) as [
    TSectionId,
    ...TSectionId[],
  ]

  /**
   * Get navigation items for sidebar, with an optional badge per section
   */
  function getSectionNavItems(
    t: TFunction,
    badges?: Partial<Record<TSectionId, string>>
  ) {
    return config.sections.map((section) => ({
      title: t(section.titleKey),
      url:
        urlStyle === 'path'
          ? `${config.basePath}/${section.id}`
          : `${config.basePath}?section=${section.id}`,
      badge: badges?.[section.id as TSectionId],
    }))
  }

  return {
    sectionIds,
    defaultSection: config.defaultSection,
    getSectionNavItems,
  }
}
