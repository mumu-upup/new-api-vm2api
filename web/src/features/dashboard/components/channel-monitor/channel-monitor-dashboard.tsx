/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  CheckCircle2,
  Clock3,
  Pause,
  Play,
  RefreshCw,
  RotateCcw,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getChannelMonitor, runChannelMonitor } from '@/features/dashboard/api'
import { PanelWrapper } from '@/features/dashboard/components/ui/panel-wrapper'
import type {
  ChannelMonitorArtifact,
  ChannelMonitorData,
  ChannelMonitorQuestion,
} from '@/features/dashboard/types'
import { formatTimestampToDate } from '@/lib/format'

import { ChannelMonitorConfigDialog } from './channel-monitor-config'
import { monitorHistory, normalizeMonitorHTML } from './monitor-work'

function pausedPreview(source: string, paused: boolean) {
  if (!paused) return source
  const style =
    '<style>*{animation-play-state:paused!important;transition:none!important}</style>'
  return source.replace(/<\/head>/i, `${style}</head>`)
}

function artifactLabel(
  t: (key: string) => string,
  id: string,
  questions: ChannelMonitorQuestion[]
) {
  const question = questions.find((item) => item.id === id)
  if (question) {
    return question.matcher === 'builtin' ? t(question.name) : question.name
  }
  if (id === 'candy-shape-matching') return t('Candy shape matching')
  if (id === 'pelican-svg-animation') return t('SVG pelican animation')
  return id
}

function MonitorPreview(props: { artifact: ChannelMonitorArtifact }) {
  const { t } = useTranslation()
  const [paused, setPaused] = useState(false)
  const isHTML = props.artifact.artifact_type === 'html'
  if (isHTML && !props.artifact.content.trim()) {
    return <EmptyState title={t('No response content')} bordered />
  }
  if (!isHTML) {
    return (
      <div className='bg-muted/40 rounded-lg border p-6 text-center text-sm'>
        <div className='mb-2 font-medium'>{t('Text result')}</div>
        <pre className='text-left whitespace-pre-wrap'>
          {props.artifact.content || t('No response content')}
        </pre>
      </div>
    )
  }
  return (
    <div className='space-y-2'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-muted-foreground text-xs'>
          {t('检测作品预览')}
        </span>
        <Button
          variant='outline'
          size='sm'
          onClick={() => setPaused((value) => !value)}
        >
          {paused ? (
            <Play className='mr-1 size-3.5' />
          ) : (
            <Pause className='mr-1 size-3.5' />
          )}
          {paused ? t('Resume preview') : t('Pause preview')}
        </Button>
      </div>
      <iframe
        className='h-[60vh] min-h-80 w-full rounded-lg border bg-white'
        sandbox='allow-scripts'
        title={t('检测作品预览')}
        srcDoc={pausedPreview(
          normalizeMonitorHTML(props.artifact.content),
          paused
        )}
      />
      <details className='rounded-lg border px-3 py-2'>
        <summary className='cursor-pointer text-sm font-medium'>
          {t('View work source')}
        </summary>
        <pre className='bg-muted/40 mt-2 max-h-64 overflow-auto rounded p-3 text-xs whitespace-pre-wrap'>
          {props.artifact.content}
        </pre>
      </details>
    </div>
  )
}

export function WorkDialog(props: {
  card: ChannelMonitorData['cards'][string]
  questions: ChannelMonitorQuestion[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [selectedModel, setSelectedModel] = useState(props.card.models[0] ?? '')
  const [question, setQuestion] = useState(props.card.question_ids[0] ?? '')
  const [selected, setSelected] = useState(0)
  const works = useMemo(
    () => monitorHistory(props.card.artifacts, selectedModel, question),
    [props.card.artifacts, selectedModel, question]
  )
  const artifact = works[selected] ?? works[0]
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={`${t('降智检测')} · ${props.card.group}`}
      description={t('Compare up to 10 works for each model and question.')}
      contentClassName='sm:max-w-6xl'
    >
      <div className='space-y-4'>
        <Select
          items={props.card.models.map((model) => ({
            value: model,
            label: model,
          }))}
          value={selectedModel}
          onValueChange={(value) => {
            if (value) {
              setSelectedModel(value)
              setSelected(0)
            }
          }}
        >
          <SelectTrigger aria-label={t('Model')} className='w-full sm:w-80'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {props.card.models.map((model) => (
              <SelectItem key={model} value={model}>
                {model}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className='flex flex-wrap gap-2'>
          {props.card.question_ids.map((id) => (
            <Button
              key={id}
              size='sm'
              aria-pressed={id === question}
              variant={id === question ? 'default' : 'outline'}
              onClick={() => {
                setQuestion(id)
                setSelected(0)
              }}
            >
              {artifactLabel(t, id, props.questions)}
            </Button>
          ))}
        </div>
        <div className='flex flex-wrap gap-2'>
          {works.map((item, index) => (
            <Button
              key={item.id}
              size='sm'
              aria-pressed={index === selected}
              variant={index === selected ? 'secondary' : 'ghost'}
              onClick={() => setSelected(index)}
            >
              {index === 0
                ? t('Latest work')
                : t('Work {{number}}', { number: index + 1 })}
            </Button>
          ))}
        </div>
        {artifact ? (
          <>
            <div className='text-muted-foreground flex flex-wrap gap-3 text-xs'>
              <span>{artifact.model}</span>
              <span>{artifact.channel_name}</span>
              <span>
                {t('Generation time')}: {artifact.duration_ms}ms
              </span>
              <span>
                {t('Detection time')}:{' '}
                {formatTimestampToDate(artifact.created_at)}
              </span>
            </div>
            {artifact.error && (
              <p role='alert' className='text-destructive text-sm'>
                {artifact.error}
              </p>
            )}
            <MonitorPreview key={artifact.id} artifact={artifact} />
          </>
        ) : (
          <p className='text-muted-foreground py-8 text-center'>
            {t(
              'No works yet. Results will appear after the next scheduled run.'
            )}
          </p>
        )}
      </div>
    </Dialog>
  )
}

function resultColor(artifact: ChannelMonitorArtifact) {
  if (artifact.status !== 'success') return 'bg-destructive/70'
  if (artifact.evaluation === 'passed') return 'bg-emerald-500'
  return 'bg-amber-500'
}

export function ChannelMonitorDashboard() {
  const { t } = useTranslation()
  const [range, setRange] = useState<'90m' | 'today'>('90m')
  const [group, setGroup] = useState('all')
  const [model, setModel] = useState('all')
  const [selectedCardKey, setSelectedCardKey] = useState<string | null>(null)
  const runMutation = useMutation({ mutationFn: runChannelMonitor })
  const query = useQuery({
    queryKey: ['dashboard', 'channel-monitor', range, group, model],
    queryFn: () =>
      getChannelMonitor({
        range,
        group: group === 'all' ? undefined : group,
        model: model === 'all' ? undefined : model,
      }),
    select: (response) => response.data,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
  })
  const data = query.data
  const selectedCard = selectedCardKey
    ? data?.cards[selectedCardKey]
    : undefined
  const cards = Object.values(data?.cards ?? {})
  const artifacts = data?.detail.artifacts ?? []
  const passed = artifacts.filter((item) => item.status === 'success').length
  const successRate = artifacts.length
    ? Math.round((passed / artifacts.length) * 100)
    : 0
  const groups = Object.keys(data?.groups ?? {}).filter(Boolean)
  const models = Object.keys(data?.models ?? {}).filter(Boolean)
  const reset = () => {
    setGroup('all')
    setModel('all')
  }
  return (
    <div className='space-y-3 sm:space-y-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <Tabs
          value={range}
          onValueChange={(value) => setRange(value as '90m' | 'today')}
        >
          <TabsList>
            <TabsTrigger value='90m'>{t('90 minutes')}</TabsTrigger>
            <TabsTrigger value='today'>{t('Today')}</TabsTrigger>
          </TabsList>
        </Tabs>
        <Select
          items={[
            { value: 'all', label: t('All groups') },
            ...groups.map((value) => ({ value, label: value })),
          ]}
          value={group}
          onValueChange={(value) => setGroup(value ?? 'all')}
        >
          <SelectTrigger className='w-44'>
            <SelectValue placeholder={t('Platform / group')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='all'>{t('All groups')}</SelectItem>
            {groups.map((item) => (
              <SelectItem key={item} value={item}>
                {item}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          items={[
            { value: 'all', label: t('All models') },
            ...models.map((value) => ({ value, label: value })),
          ]}
          value={model}
          onValueChange={(value) => setModel(value ?? 'all')}
        >
          <SelectTrigger className='w-44'>
            <SelectValue placeholder={t('Model')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='all'>{t('All models')}</SelectItem>
            {models.map((item) => (
              <SelectItem key={item} value={item}>
                {item}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          variant='ghost'
          size='icon'
          aria-label={t('Reset')}
          onClick={reset}
        >
          <RotateCcw />
        </Button>
        <Button
          variant='ghost'
          size='icon'
          aria-label={t('Refresh')}
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          <RefreshCw className={query.isFetching ? 'animate-spin' : ''} />
        </Button>
        <ChannelMonitorConfigDialog />
        <Button
          variant='outline'
          disabled={runMutation.isPending}
          onClick={() => runMutation.mutate()}
        >
          {t('Run monitor')}
        </Button>
      </div>
      {runMutation.isSuccess && (
        <p role='status' className='text-muted-foreground text-sm'>
          {t('Monitor task queued. Results appear after generation finishes.')}
        </p>
      )}
      <p className='text-muted-foreground text-xs'>
        {t(
          'Each group uses its configured questions and interval. Up to 10 works per model and question are retained until Beijing midnight.'
        )}
      </p>
      <PanelWrapper
        title={t('Channel monitor metrics')}
        description={t(
          'Success rate and response timing for the selected window.'
        )}
        loading={query.isLoading}
        empty={!query.isLoading && artifacts.length === 0}
      >
        <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
          <Metric label={t('Success rate')} value={`${successRate}%`} />
          <Metric
            label={t('Error rate')}
            value={`${Math.max(0, 100 - successRate)}%`}
          />
          <Metric label={t('Works')} value={String(artifacts.length)} />
          <Metric
            label={t('AVG latency')}
            value={`${Math.round(artifacts.reduce((sum, item) => sum + item.duration_ms, 0) / Math.max(1, artifacts.length))}ms`}
          />
        </div>
        <div
          className='mt-4 flex h-8 gap-0.5 overflow-hidden rounded'
          aria-label={t('Five minute channel matrix')}
        >
          {artifacts.slice(0, 90).map((item) => (
            <span
              key={item.id}
              className={`min-w-1 flex-1 ${resultColor(item)}`}
              title={`${item.channel_name} ${item.model}`}
            />
          ))}
        </div>
      </PanelWrapper>
      <PanelWrapper
        title={t('降智检测 作品对比')}
        description={t('Compare up to 10 works for each model and question.')}
        loading={query.isLoading}
        empty={!query.isLoading && cards.length === 0}
      >
        <div className='grid gap-3 md:grid-cols-2'>
          {cards.map((card) => {
            const current = card.artifacts[0]
            return (
              <div key={card.key} className='rounded-xl border p-4'>
                <div className='flex items-start justify-between gap-3'>
                  <div>
                    <div className='font-semibold'>
                      {card.group || t('default')}
                    </div>
                    <div className='text-muted-foreground mt-1 text-xs'>
                      {t('Every {{minutes}} minutes', {
                        minutes: card.interval_minutes,
                      })}{' '}
                      · {t('{{count}} models', { count: card.models.length })}
                    </div>
                  </div>
                  <StatusBadge
                    label={
                      current?.evaluation === 'passed'
                        ? t('Passed')
                        : t('Needs review')
                    }
                    variant={
                      current?.evaluation === 'passed' ? 'success' : 'warning'
                    }
                    copyable={false}
                  />
                </div>
                <div className='text-muted-foreground mt-3 flex flex-wrap items-center gap-2 text-xs'>
                  <CheckCircle2 className='size-3.5' />
                  {current ? t('检测作品已更新') : t('No data available')}
                  <Clock3 className='ml-2 size-3.5' />
                  {current
                    ? formatTimestampToDate(current.created_at)
                    : t('N/A')}
                </div>
                <div className='mt-3 flex flex-wrap gap-1.5'>
                  {card.question_ids.map((id) => (
                    <span
                      key={id}
                      className='bg-muted rounded px-2 py-1 text-xs'
                    >
                      {artifactLabel(t, id, data?.questions ?? [])}
                    </span>
                  ))}
                </div>
                <Button
                  className='mt-4 w-full'
                  variant='outline'
                  onClick={() => {
                    setSelectedCardKey(card.key)
                  }}
                >
                  {t('View work')}
                </Button>
              </div>
            )
          })}
        </div>
      </PanelWrapper>
      <PanelWrapper
        title={t('Model details')}
        description={t('Latest channel monitor results by model.')}
        empty={!query.isLoading && artifacts.length === 0}
      >
        <div className='overflow-x-auto'>
          <table className='w-full text-left text-sm'>
            <thead>
              <tr className='text-muted-foreground border-b text-xs'>
                <th className='px-2 py-2'>{t('Platform / model')}</th>
                <th className='px-2 py-2'>{t('Success rate')}</th>
                <th className='px-2 py-2'>{t('AVG latency')}</th>
                <th className='px-2 py-2'>{t('Status')}</th>
              </tr>
            </thead>
            <tbody>
              {models.map((item) => {
                const rows = artifacts.filter(
                  (artifact) => artifact.model === item
                )
                const ok = rows.filter(
                  (artifact) => artifact.status === 'success'
                ).length
                let status = t('No data available')
                if (rows.length > 0) {
                  status = ok === rows.length ? t('Healthy') : t('Needs review')
                }
                return (
                  <tr key={item} className='border-b last:border-0'>
                    <td className='px-2 py-2'>{item}</td>
                    <td className='px-2 py-2'>
                      {Math.round((ok / Math.max(1, rows.length)) * 100)}%
                    </td>
                    <td className='px-2 py-2'>
                      {Math.round(
                        rows.reduce(
                          (sum, artifact) => sum + artifact.duration_ms,
                          0
                        ) / Math.max(1, rows.length)
                      )}
                      ms
                    </td>
                    <td className='px-2 py-2'>{status}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </PanelWrapper>
      {selectedCard && (
        <WorkDialog
          key={selectedCard.key}
          card={selectedCard}
          questions={data?.questions ?? []}
          open={Boolean(selectedCard)}
          onOpenChange={(open) => {
            if (!open) setSelectedCardKey(null)
          }}
        />
      )}
    </div>
  )
}

function Metric(props: { label: string; value: string }) {
  return (
    <div className='rounded-lg border p-3'>
      <div className='text-muted-foreground text-xs'>{props.label}</div>
      <div className='mt-1 text-xl font-semibold'>{props.value}</div>
    </div>
  )
}
