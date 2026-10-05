import { createFileRoute, redirect } from '@tanstack/react-router'

import { GptAccounts } from '@/features/gpt-accounts'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/gpt-accounts/')({
  beforeLoad: () => {
    if ((useAuthStore.getState().auth.user?.role ?? 0) < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  component: GptAccounts,
})
