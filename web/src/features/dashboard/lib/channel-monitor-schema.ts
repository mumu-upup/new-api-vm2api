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

export const channelMonitorConfigSchema = z.object({
  groups: z.array(
    z
      .object({
        key: z.string().min(1).max(128),
        enabled: z.boolean(),
        question_ids: z.array(z.string()),
        interval_minutes: z.number().int().min(1).max(1440),
      })
      .refine((group) => !group.enabled || group.question_ids.length > 0)
  ),
  questions: z
    .array(
      z
        .object({
          id: z.string().regex(/^[a-zA-Z0-9_-]{1,64}$/),
          name: z.string().trim().min(1).max(128),
          prompt: z.string().trim().min(1).max(16000),
          artifact_type: z.enum(['html', 'text']),
          matcher: z.enum(['none', 'exact', 'contains', 'regex']),
          expected: z.string().max(4000),
        })
        .refine(
          (question) =>
            question.matcher === 'none' || question.expected.trim().length > 0
        )
    )
    .max(50),
})
