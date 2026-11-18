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
import { z } from 'zod'

import {
  ADMIN_ROLE_KEY,
  DEPUTY_ROLE_KEY,
  type PermissionCatalog,
  type AdminPermissionMatrix,
  normalizeAdminPermissions,
  withoutRechargeResources,
} from '@/lib/admin-permissions'
import { quotaUnitsToDollars } from '@/lib/format'
import { ROLE, type AdminRole } from '@/lib/roles'

import { DEFAULT_GROUP } from '../constants'
import type { UserFormData, User } from '../types'

// ============================================================================
// Form Schema
// ============================================================================

export const userFormSchema = z.object({
  username: z.string().min(1, 'Username is required'),
  display_name: z.string().optional(),
  password: z.string().optional(),
  role: z.number().optional(),
  // With role 10: on create, makes a deputy admin instead of a full admin; on
  // update, the kind GET /api/user/:id reports (changed only by row actions).
  admin_role: z.enum(['deputy']).optional(),
  quota_dollars: z.number().min(0).optional(),
  group: z.string().optional(),
  remark: z.string().optional(),
  admin_permissions: z
    .record(z.string(), z.record(z.string(), z.boolean()))
    .optional(),
})

export type UserFormValues = z.infer<typeof userFormSchema>

// ============================================================================
// Form Defaults
// ============================================================================

export const USER_FORM_DEFAULT_VALUES: UserFormValues = {
  username: '',
  display_name: '',
  password: '',
  role: 1, // Default to common user
  admin_role: undefined,
  quota_dollars: 0,
  group: DEFAULT_GROUP,
  remark: '',
  // Filled against the backend catalog at render time; see UsersMutateDrawer.
  admin_permissions: {},
}

// ============================================================================
// Admin Permission Editor
// ============================================================================

/**
 * The catalog and baseline role used to edit the permissions of a target
 * administrator. A deputy admin never sees, holds or sends recharge resources,
 * and its missing cells default to the deputy baseline.
 */
export function adminPermissionEditorScope(
  catalog: PermissionCatalog,
  adminRole?: AdminRole
): { catalog: PermissionCatalog; roleKey: string } {
  if (adminRole === 'deputy') {
    return {
      catalog: withoutRechargeResources(catalog),
      roleKey: DEPUTY_ROLE_KEY,
    }
  }
  return { catalog, roleKey: ADMIN_ROLE_KEY }
}

/** The full matrix of a target administrator, as the editor shows and sends it. */
export function normalizeTargetPermissions(
  value: AdminPermissionMatrix | null | undefined,
  catalog: PermissionCatalog,
  adminRole?: AdminRole
): AdminPermissionMatrix {
  const scope = adminPermissionEditorScope(catalog, adminRole)
  return normalizeAdminPermissions(value, scope.catalog, scope.roleKey)
}

// ============================================================================
// Form Data Transformation
// ============================================================================

/**
 * Transform form data to API payload. `adminRole` is the kind of the target
 * administrator (the form's choice on create, the user's on update).
 */
export function transformFormDataToPayload(
  data: UserFormValues,
  userId?: number,
  catalog?: PermissionCatalog,
  adminRole?: AdminRole
): UserFormData & { id?: number } {
  const payload: UserFormData & { id?: number } = {
    username: data.username,
    display_name: data.display_name || data.username,
    password: data.password || undefined,
  }

  const role = userId === undefined ? data.role || 1 : (data.role ?? 0)

  // Only send the permission matrix when the target is an admin and the catalog
  // is available; without the catalog we cannot build a full matrix, so we omit
  // the field (the backend then leaves existing permissions untouched).
  if (role >= ROLE.ADMIN && catalog) {
    payload.admin_permissions = normalizeTargetPermissions(
      data.admin_permissions as AdminPermissionMatrix | undefined,
      catalog,
      adminRole
    )
  }

  // For create: only send required fields
  if (userId === undefined) {
    payload.role = role
    if (role === ROLE.ADMIN && adminRole === 'deputy') {
      payload.admin_role = 'deputy'
    }
  } else {
    // For update: quota is adjusted atomically via /api/user/manage, not sent here
    payload.group = data.group
    payload.remark = data.remark || undefined
    payload.id = userId
  }

  return payload
}

/**
 * Transform user data to form defaults. The admin permission matrix is passed
 * through as-is (the backend already returns a full matrix); it is filled against
 * the catalog at render time in UsersMutateDrawer.
 */
export function transformUserToFormDefaults(user: User): UserFormValues {
  return {
    username: user.username,
    display_name: user.display_name,
    password: '',
    role: user.role,
    admin_role:
      user.role === ROLE.ADMIN && user.admin_role === 'deputy'
        ? 'deputy'
        : undefined,
    quota_dollars: quotaUnitsToDollars(user.quota),
    group: user.group || DEFAULT_GROUP,
    remark: user.remark || '',
    admin_permissions: user.admin_permissions ?? {},
  }
}
