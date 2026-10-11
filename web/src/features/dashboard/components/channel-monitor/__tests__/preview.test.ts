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
import { describe, expect, it } from 'vitest'

import type { ChannelMonitorArtifact } from '@/features/dashboard/types'

import { normalizeMonitorHTML, monitorHistory } from '../monitor-work'

describe('monitor work preview', () => {
  it('renders fenced SVG fragments in an isolated complete document', () => {
    const source = normalizeMonitorHTML(
      '```svg\n<svg><animate dur="1s"/></svg>\n```'
    )
    expect(source).toContain('<!doctype html>')
    const document = new DOMParser().parseFromString(source, 'text/html')
    expect(document.querySelector('svg animate')?.getAttribute('dur')).toBe('1s')
    expect(source).not.toContain('```')
    expect(source).toContain('connect-src')
  })
  it('preserves inline animation scripts and strips surrounding explanations', () => {
    const source = normalizeMonitorHTML(
      'Here is the page:\n```html\n<html><body><svg/><script>animate()</script></body></html>\n```\nDone'
    )
    expect(source).toContain('<script>animate()</script>')
    expect(source).not.toContain('Here is the page')
  })
  it('preserves animation styles before an SVG fragment', () => {
    expect(
      normalizeMonitorHTML(
        '<style>@keyframes spin{to{transform:rotate(360deg)}}</style><svg/>'
      )
    ).toContain('@keyframes spin')
  })
  it('adds responsive overflow styles so oversized SVG artwork remains visible', () => {
    const source = normalizeMonitorHTML(
      '<svg width="2400" height="1600" viewBox="0 0 2400 1600"><rect width="2400" height="1600"/></svg>'
    )
    const document = new DOMParser().parseFromString(source, 'text/html')
    const previewStyle = [...document.head.querySelectorAll('style')]
      .map((style) => style.textContent ?? '')
      .join('\n')

    expect(previewStyle).toContain('overflow: auto')
    expect(previewStyle).toContain('max-width: 100%')
    expect(previewStyle).toContain('height: auto')
  })
  it('enforces the preview policy before scripts despite misleading head comments', () => {
    const source = normalizeMonitorHTML(
      '<!doctype html><html><!-- <head> --><script>animate()</script><head></head><body><svg/></body></html>'
    )
    const document = new DOMParser().parseFromString(source, 'text/html')
    const policy = document.head.firstElementChild
    expect(policy?.tagName).toBe('META')
    expect(policy?.getAttribute('http-equiv')).toBe('Content-Security-Policy')
    expect(policy?.getAttribute('content')).toContain("connect-src 'none'")
    const script = document.querySelector('script')
    expect(script?.textContent).toBe('animate()')
    expect(script && policy?.compareDocumentPosition(script)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING
    )
  })
  it('removes document redirects and base URLs without removing animation content', () => {
    const source = normalizeMonitorHTML(
      '<html><head><base href="https://example.test/" target="_top"><meta http-equiv="rEfReSh" content="0; url=https://example.test/"></head><body><svg><animate dur="1s"/></svg></body></html>'
    )
    const document = new DOMParser().parseFromString(source, 'text/html')
    expect(document.querySelector('base')).toBeNull()
    expect(
      [...document.querySelectorAll('meta[http-equiv]')].some(
        (meta) => meta.getAttribute('http-equiv')?.toLowerCase() === 'refresh'
      )
    ).toBe(false)
    expect(document.querySelector('svg animate')?.getAttribute('dur')).toBe('1s')
  })
  it('retains ten newest works for the selected question and model', () => {
    const records = Array.from(
      { length: 12 },
      (_, index) =>
        ({
          id: index,
          created_at: index,
          model: 'a',
          preset_id: 'q',
        }) as ChannelMonitorArtifact
    )
    records.push({
      id: 99,
      created_at: 99,
      model: 'b',
      preset_id: 'q',
    } as ChannelMonitorArtifact)
    expect(monitorHistory(records, 'a', 'q').map((item) => item.id)).toEqual([
      11, 10, 9, 8, 7, 6, 5, 4, 3, 2,
    ])
  })
})
