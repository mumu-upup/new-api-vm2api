import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, renderHook, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { useSidebarData } from '@/hooks/use-sidebar-data'
import { useSidebarView } from '@/hooks/use-sidebar-view'
import { Route as StatusCheckRoute } from '@/routes/_authenticated/gpt-account-status-check'
import { Route } from '@/routes/_authenticated/gpt-accounts'
import { useAuthStore } from '@/stores/auth-store'

afterEach(() => useAuthStore.getState().auth.reset())

function Navigation() {
  const view = useSidebarView()
  return (
    <nav aria-label='Main'>
      {view.navGroups
        .flatMap((group) => group.items)
        .map((item) => (
          <span key={item.title}>{item.title}</span>
        ))}
    </nav>
  )
}

it('places GPT Accounts in the admin navigation group', () => {
  const { result } = renderHook(useSidebarData)
  const admin = result.current.navGroups.find((group) => group.id === 'admin')
  expect(admin?.items).toContainEqual(
    expect.objectContaining({ url: '/gpt-accounts', requiredRole: 10 })
  )
  expect(
    result.current.navGroups
      .filter((group) => group.id !== 'admin')
      .flatMap((group) => group.items)
  ).not.toContainEqual(expect.objectContaining({ url: '/gpt-accounts' }))
})

it.each([1, 10, 100])(
  'role %i sees GPT Accounts only with admin access and the route enforces the same boundary',
  async (role) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'viewer', role })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    client.setQueryData(['status'], { SidebarModulesAdmin: '' })
    const root = createRootRoute({ component: Navigation })
    const router = createRouter({
      routeTree: root,
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
    await router.load()
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
    await screen.findByRole('navigation', { name: 'Main' })
    if (role < 10) {
      expect(screen.queryByText('GPT Accounts')).not.toBeInTheDocument()
      expect(
        screen.queryByText('GPT Account Status Check')
      ).not.toBeInTheDocument()
      expect(() => StatusCheckRoute.options.beforeLoad?.({} as never)).toThrow()
      expect(() => Route.options.beforeLoad?.({} as never)).toThrow()
    } else {
      expect(screen.getByText('GPT Accounts')).toBeVisible()
      expect(screen.getByText('GPT Account Status Check')).toBeVisible()
      expect(() =>
        StatusCheckRoute.options.beforeLoad?.({} as never)
      ).not.toThrow()
      expect(() => Route.options.beforeLoad?.({} as never)).not.toThrow()
    }
    client.clear()
  }
)
