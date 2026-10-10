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
import type { ChannelMonitorArtifact } from '@/features/dashboard/types'

export function normalizeMonitorHTML(source: string): string {
  const fenced = source.match(/```(?:html|svg|xml)?\s*\n([\s\S]*?)```/i)
  let html = (fenced?.[1] ?? source)
    .trim()
    .replace(/^```(?:html|svg|xml)?\s*/i, '')
    .replace(/```\s*$/, '')
  const start = html.search(/<(?:!doctype\s+html|[a-z][\w:-]*)(?:\s|>|\/)/i)
  if (start > 0) html = html.slice(start)
  const security =
    "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; base-uri 'none'; form-action 'none'"
  // Parse into an inert document with the policy preceding any supplied markup.
  // Regex insertion can mistake comments or script contents for the real head.
  const document = new DOMParser().parseFromString(
    `<meta http-equiv="Content-Security-Policy" content="${security}">${html}`,
    'text/html'
  )
  for (const element of document.querySelectorAll('base, meta[http-equiv]')) {
    const directive = element.getAttribute('http-equiv')?.trim().toLowerCase()
    if (
      element.tagName === 'BASE' ||
      directive === 'refresh' ||
      directive === 'content-security-policy'
    ) {
      element.remove()
    }
  }
  const policy = document.createElement('meta')
  policy.setAttribute('http-equiv', 'Content-Security-Policy')
  policy.setAttribute('content', security)
  const charset = document.createElement('meta')
  charset.setAttribute('charset', 'utf-8')
  const viewport = document.createElement('meta')
  viewport.setAttribute('name', 'viewport')
  viewport.setAttribute('content', 'width=device-width,initial-scale=1')
  document.head.prepend(policy, charset, viewport)
  return `<!doctype html>${document.documentElement.outerHTML}`
}

export function monitorHistory(
  artifacts: ChannelMonitorArtifact[],
  model: string,
  question: string
): ChannelMonitorArtifact[] {
  return artifacts
    .filter((item) => item.model === model && item.preset_id === question)
    .sort((a, b) => b.created_at - a.created_at || b.id - a.id)
    .slice(0, 10)
}
