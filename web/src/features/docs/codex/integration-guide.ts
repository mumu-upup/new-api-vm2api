/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import { MODELVIVID_BRAND } from '@/lib/modelvivid-brand'

export const DOC_SECTIONS = [
  { id: 'quick-start', title: 'Quick start' },
  { id: 'models', title: 'Available models' },
  { id: 'api-key', title: 'API key' },
  { id: 'base-url', title: 'Base URL' },
  { id: 'codex-cli', title: 'Codex CLI' },
  { id: 'openai-compatible', title: 'OpenAI-compatible SDKs' },
  { id: 'claude-code', title: 'Claude Code' },
  { id: 'first-request', title: 'First request' },
  { id: 'troubleshooting', title: 'Troubleshooting' },
] as const

export type CodeExamples = {
  baseUrl: string
  curl: string
  codexMacOS: string
  codexPowerShell: string
  python: string
  node: string
  claude: string
}

export function buildCodeExamples(modelId = 'YOUR_MODEL_ID'): CodeExamples {
  const baseUrl = MODELVIVID_BRAND.apiBaseUrl
  const request = `{"model":"${modelId}","messages":[{"role":"user","content":"Say hello"}]}`
  return {
    baseUrl,
    curl: `curl ${baseUrl}/chat/completions \\
  -H "Authorization: Bearer YOUR_MODELVIVID_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '${request}'`,
    codexMacOS: `export OPENAI_API_KEY=YOUR_MODELVIVID_API_KEY\nexport OPENAI_BASE_URL=${baseUrl}\ncodex --model ${modelId}`,
    codexPowerShell: `$env:OPENAI_API_KEY="YOUR_MODELVIVID_API_KEY"\n$env:OPENAI_BASE_URL="${baseUrl}"\ncodex --model ${modelId}`,
    python: `from openai import OpenAI\n\nclient = OpenAI(\n    api_key="YOUR_MODELVIVID_API_KEY",\n    base_url="${baseUrl}",\n)\nresponse = client.chat.completions.create(\n    model="${modelId}",\n    messages=[{"role": "user", "content": "Say hello"}],\n)\nprint(response.choices[0].message.content)`,
    node: `import OpenAI from "openai";\n\nconst client = new OpenAI({\n  apiKey: "YOUR_MODELVIVID_API_KEY",\n  baseURL: "${baseUrl}",\n});\nconst response = await client.chat.completions.create({\n  model: "${modelId}",\n  messages: [{ role: "user", content: "Say hello" }],\n});\nconsole.log(response.choices[0].message.content);`,
    claude: `export ANTHROPIC_BASE_URL=${baseUrl}\nexport ANTHROPIC_AUTH_TOKEN=YOUR_MODELVIVID_API_KEY\n# Keep the URL exactly as shown; do not append another /v1.`,
  }
}
