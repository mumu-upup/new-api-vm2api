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
import { useContext, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { ConcurrencyStatus } from '@/features/concurrency/concurrency-status'
import { UserConcurrencyContext } from '@/features/concurrency/use-request-concurrency'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { isUserDeleted } from '../constants'
import type { User } from '../types'
import { UserConcurrencyDialog } from './user-concurrency-dialog'

export function UserConcurrencyCell(props: { user: User }) {
  const { t } = useTranslation()
  const viewerRole = useAuthStore(
    (state) => state.auth.user?.role ?? ROLE.GUEST
  )
  const canManageUser =
    viewerRole === ROLE.SUPER_ADMIN ||
    (viewerRole >= ROLE.ADMIN && viewerRole > props.user.role)
  const concurrency = useContext(UserConcurrencyContext)?.[props.user.id]
  const [editingLimit, setEditingLimit] = useState<number | null>(null)
  return (
    <div className='flex flex-col items-start gap-1'>
      <ConcurrencyStatus
        current={concurrency?.current}
        limit={concurrency?.max_concurrent_requests}
      />
      {canManageUser && !isUserDeleted(props.user) && (
        <Button
          variant='link'
          size='sm'
          className='h-auto px-1.5 py-0 text-xs font-normal'
          onClick={() =>
            concurrency && setEditingLimit(concurrency.max_concurrent_requests)
          }
          disabled={!concurrency}
        >
          {t('Edit concurrency limit')}
        </Button>
      )}
      {canManageUser && editingLimit !== null && (
        <UserConcurrencyDialog
          userId={props.user.id}
          username={props.user.username}
          limit={editingLimit}
          onOpenChange={(open) => !open && setEditingLimit(null)}
        />
      )}
    </div>
  )
}
