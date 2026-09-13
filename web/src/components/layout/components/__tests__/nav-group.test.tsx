import { render, screen } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { SidebarProvider } from '@/components/ui/sidebar'

import { NavGroup } from '../nav-group'

vi.mock('@tanstack/react-router', () => ({
  Link: (props: ComponentProps<'a'>) => <a {...props} />,
  useLocation: ({
    select,
  }: {
    select: (location: { href: string }) => string
  }) => select({ href: '/wallet' }),
}))

describe('sidebar external links', () => {
  it('renders external navigation items as safe links', () => {
    render(
      <SidebarProvider>
        <NavGroup
          title='Personal'
          items={[
            {
              title: 'Store',
              url: 'https://wzyp.cn/shop/OFYRDROX',
              external: true,
            },
          ]}
        />
      </SidebarProvider>
    )

    expect(screen.getByRole('link', { name: 'Store' })).toHaveAttribute(
      'href',
      'https://wzyp.cn/shop/OFYRDROX'
    )
    expect(screen.getByRole('link', { name: 'Store' })).toHaveAttribute(
      'target',
      '_blank'
    )
    expect(screen.getByRole('link', { name: 'Store' })).toHaveAttribute(
      'rel',
      'noopener noreferrer'
    )
  })
})
