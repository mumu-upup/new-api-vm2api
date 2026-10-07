import { createFileRoute, redirect } from '@tanstack/react-router'

import { GptAccountStatusCheck } from '@/features/gpt-account-status-check'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute(
  '/_authenticated/gpt-account-status-check/'
)({
  beforeLoad: () => {
    if ((useAuthStore.getState().auth.user?.role ?? 0) < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  component: GptAccountStatusCheck,
})
