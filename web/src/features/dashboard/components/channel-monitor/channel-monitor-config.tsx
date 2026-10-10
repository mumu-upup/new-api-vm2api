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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Settings2, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useFieldArray, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import {
  getChannelMonitorConfig,
  updateChannelMonitorConfig,
} from '@/features/dashboard/api'
import { channelMonitorConfigSchema } from '@/features/dashboard/lib/channel-monitor-schema'
import type {
  ChannelMonitorConfig,
  ChannelMonitorConfigData,
} from '@/features/dashboard/types'

export function ChannelMonitorConfigForm(props: {
  data: ChannelMonitorConfigData
  onSave: (config: ChannelMonitorConfig) => void
  pending: boolean
}) {
  const { t } = useTranslation()
  const form = useForm<z.infer<typeof channelMonitorConfigSchema>>({
    resolver: zodResolver(channelMonitorConfigSchema),
    defaultValues: {
      ...props.data.config,
      questions: props.data.config.questions.filter(
        (q) => q.matcher !== 'builtin'
      ) as z.infer<typeof channelMonitorConfigSchema>['questions'],
    },
  })
  const questions = useFieldArray({
    control: form.control,
    name: 'questions',
    keyName: 'fieldKey',
  })
  const values = form.watch()
  const allQuestions = [...props.data.builtin_questions, ...values.questions]
  const matcherOptions = [
    { value: 'none', label: t('Preview only') },
    { value: 'exact', label: t('Exact match') },
    { value: 'contains', label: t('Contains') },
    { value: 'regex', label: t('Regular expression') },
  ]
  return (
    <form onSubmit={form.handleSubmit(props.onSave)} className='space-y-5'>
      <fieldset disabled={props.pending} className='space-y-5'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Choose visible groups, questions and execution intervals. New groups remain disabled after saving.'
          )}
        </p>
        {values.groups.length === 0 && <p>{t('No data available')}</p>}
        {values.groups.map((group, index) => (
          <section key={group.key} className='space-y-3 rounded-lg border p-4'>
            <label className='flex items-center gap-2 font-medium'>
              <Checkbox
                checked={group.enabled}
                disabled={props.pending}
                onCheckedChange={(checked) =>
                  form.setValue(`groups.${index}.enabled`, checked)
                }
              />
              {group.key}
            </label>
            <label className='flex items-center gap-3 text-sm'>
              {t('Execution interval (minutes)')}
              <Input
                aria-label={`${group.key} ${t('Execution interval (minutes)')}`}
                className='w-28'
                type='number'
                min={1}
                max={1440}
                step={1}
                {...form.register(`groups.${index}.interval_minutes`, {
                  valueAsNumber: true,
                })}
              />
            </label>
            <div className='flex flex-wrap gap-4'>
              {allQuestions.map((q) => (
                <label key={q.id} className='flex items-center gap-2 text-sm'>
                  <Checkbox
                    checked={group.question_ids.includes(q.id)}
                    disabled={props.pending}
                    onCheckedChange={(checked) =>
                      form.setValue(
                        `groups.${index}.question_ids`,
                        checked
                          ? [...group.question_ids, q.id]
                          : group.question_ids.filter((id) => id !== q.id)
                      )
                    }
                  />
                  {q.matcher === 'builtin'
                    ? t(q.name)
                    : q.name || t('Custom question')}
                </label>
              ))}
            </div>
          </section>
        ))}
        <div className='flex items-center justify-between'>
          <h3 className='font-semibold'>{t('Custom questions')}</h3>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={questions.fields.length >= 50}
            onClick={() =>
              questions.append({
                id: `custom-${crypto.randomUUID()}`,
                name: '',
                prompt: '',
                artifact_type: 'html',
                matcher: 'none',
                expected: '',
              })
            }
          >
            <Plus className='size-4' />
            {t('Add question')}
          </Button>
        </div>
        {questions.fields.map((field, index) => (
          <section
            key={field.fieldKey}
            className='space-y-3 rounded-lg border p-4'
          >
            <div className='flex gap-2'>
              <label className='flex-1 space-y-1 text-sm'>
                {t('Question name')}
                <Input
                  maxLength={128}
                  {...form.register(`questions.${index}.name`)}
                />
              </label>
              <Button
                className='self-end'
                type='button'
                variant='ghost'
                size='icon'
                aria-label={t('Delete question')}
                onClick={() => {
                  const id = values.questions[index].id
                  for (let i = 0; i < values.groups.length; i++) {
                    form.setValue(
                      `groups.${i}.question_ids`,
                      values.groups[i].question_ids.filter((q) => q !== id)
                    )
                  }
                  questions.remove(index)
                }}
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
            <label className='block space-y-1 text-sm'>
              {t('Prompt')}
              <Textarea
                maxLength={16000}
                {...form.register(`questions.${index}.prompt`)}
              />
            </label>
            <div className='grid gap-3 sm:grid-cols-2'>
              <label className='space-y-1 text-sm'>
                {t('Result type')}
                <Select
                  disabled={props.pending}
                  items={[
                    { value: 'html', label: 'HTML / SVG' },
                    { value: 'text', label: t('Text') },
                  ]}
                  value={values.questions[index].artifact_type}
                  onValueChange={(value) => {
                    if (value === 'html' || value === 'text') {
                      form.setValue(`questions.${index}.artifact_type`, value)
                    }
                  }}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='html'>HTML / SVG</SelectItem>
                    <SelectItem value='text'>{t('Text')}</SelectItem>
                  </SelectContent>
                </Select>
              </label>
              <label className='space-y-1 text-sm'>
                {t('Validation rule')}
                <Select
                  disabled={props.pending}
                  items={matcherOptions}
                  value={values.questions[index].matcher}
                  onValueChange={(value) => {
                    if (value) {
                      form.setValue(
                        `questions.${index}.matcher`,
                        value as 'none' | 'exact' | 'contains' | 'regex'
                      )
                    }
                  }}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {matcherOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>
            </div>
            {values.questions[index].matcher !== 'none' && (
              <label className='block space-y-1 text-sm'>
                {t('Expected answer or pattern')}
                <Textarea
                  maxLength={4000}
                  {...form.register(`questions.${index}.expected`)}
                />
              </label>
            )}
          </section>
        ))}
      </fieldset>
      {Object.keys(form.formState.errors).length > 0 && (
        <p role='alert' className='text-destructive text-sm'>
          {t(
            'Check question fields, select a question for each enabled group, and enter an interval from 1 to 1440 minutes.'
          )}
        </p>
      )}
      <div className='flex justify-end'>
        <Button type='submit' disabled={props.pending}>
          {props.pending ? t('Saving...') : t('Save')}
        </Button>
      </div>
    </form>
  )
}

export function ChannelMonitorConfigDialog() {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const client = useQueryClient()
  const query = useQuery({
    queryKey: ['dashboard', 'channel-monitor-config'],
    queryFn: getChannelMonitorConfig,
    enabled: open,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  const mutation = useMutation({
    mutationFn: updateChannelMonitorConfig,
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({
          queryKey: ['dashboard', 'channel-monitor-config'],
        }),
        client.invalidateQueries({
          queryKey: ['dashboard', 'channel-monitor'],
        }),
      ])
      toast.success(t('Saved successfully'))
      setOpen(false)
    },
  })
  return (
    <>
      <Button variant='outline' onClick={() => setOpen(true)}>
        <Settings2 className='size-4' />
        {t('Monitor configuration')}
      </Button>
      <Dialog
        open={open}
        onOpenChange={setOpen}
        title={t('Monitor configuration')}
        contentClassName='sm:max-w-3xl'
      >
        {query.isFetching && <p>{t('Loading...')}</p>}
        {query.isError && (
          <Button variant='outline' onClick={() => void query.refetch()}>
            {t('Retry')}
          </Button>
        )}
        {query.data && !query.isFetching && (
          <ChannelMonitorConfigForm
            key={query.dataUpdatedAt}
            data={query.data}
            pending={mutation.isPending}
            onSave={(config) => mutation.mutate(config)}
          />
        )}
      </Dialog>
    </>
  )
}
