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

import { useQuery } from '@tanstack/react-query'
import { Check, Copy, ExternalLink } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { PublicLayout } from '@/components/layout'
import { getPricing } from '@/features/pricing/api'
import type { PricingModel } from '@/features/pricing/types'
import { MODELVIVID_BRAND } from '@/lib/modelvivid-brand'

import {
  DOC_SECTIONS,
  buildCodeExamples as buildIntegrationCodeExamples,
} from './integration-guide'

function CopyCodeButton(props: { value: string; label: string }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)
  const timerRef = useRef<number | null>(null)

  useEffect(
    () => () => {
      if (timerRef.current !== null) window.clearTimeout(timerRef.current)
    },
    []
  )

  const copy = async () => {
    setCopyFailed(false)
    try {
      await navigator.clipboard.writeText(props.value)
      setCopied(true)
      if (timerRef.current !== null) window.clearTimeout(timerRef.current)
      timerRef.current = window.setTimeout(() => setCopied(false), 1600)
    } catch {
      setCopyFailed(true)
    }
  }

  return (
    <div className='flex items-center gap-2'>
      <Button
        type='button'
        size='sm'
        variant='ghost'
        onClick={copy}
        aria-label={`${t('Copy')} ${props.label}`}
      >
        {copied ? <Check /> : <Copy />}
        {copied ? t('Copied') : t('Copy')}
      </Button>
      {copyFailed && (
        <span className='text-destructive text-xs' role='status'>
          {t('Copy failed; select the code manually.')}
        </span>
      )}
    </div>
  )
}

function CodeBlock(props: { value: string; label: string }) {
  return (
    <div className='border-border/60 bg-muted/30 overflow-hidden rounded-xl border'>
      <div className='border-border/50 flex items-center justify-between border-b px-3 py-1.5'>
        <span className='text-muted-foreground text-xs'>{props.label}</span>
        <CopyCodeButton value={props.value} label={props.label} />
      </div>
      <pre className='overflow-x-auto p-4 text-xs leading-6 sm:text-sm'>
        <code>{props.value}</code>
      </pre>
    </div>
  )
}

function ModelList(props: { models: PricingModel[]; loading: boolean; error: unknown }) {
  const { t } = useTranslation()
  if (props.loading) {
    return <p className='text-muted-foreground text-sm'>{t('Loading models...')}</p>
  }
  if (props.error) {
    return (
      <p className='text-destructive text-sm' role='alert'>
        {t('Model list is temporarily unavailable. Try again shortly.')}
      </p>
    )
  }
  if (props.models.length === 0) {
    return <p className='text-muted-foreground text-sm'>{t('No models are currently enabled.')}</p>
  }
  return (
    <div className='grid gap-3 sm:grid-cols-2'>
      {props.models.map((model) => (
        <article className='border-border/60 rounded-xl border p-4' key={model.model_name}>
          <h3 className='font-medium'>{model.model_name}</h3>
          {(model.vendor_name || model.description) && (
            <p className='text-muted-foreground mt-1 line-clamp-2 text-xs'>
              {model.vendor_name || model.description}
            </p>
          )}
          {model.supported_endpoint_types?.length ? (
            <div className='mt-3 flex flex-wrap gap-1.5'>
              {model.supported_endpoint_types.map((endpoint) => (
                <span className='bg-muted rounded-full px-2 py-0.5 text-[11px]' key={endpoint}>
                  {endpoint}
                </span>
              ))}
            </div>
          ) : null}
        </article>
      ))}
    </div>
  )
}

export function CodexDocs() {
  const { t } = useTranslation()
  const pricing = useQuery({
    queryKey: ['modelvivid-docs-models'],
    queryFn: getPricing,
    staleTime: 5 * 60 * 1000,
    retry: 1,
  })
  const models = useMemo(
    () => (pricing.data?.success ? pricing.data.data : []) || [],
    [pricing.data]
  )
  const modelId = models[0]?.model_name || 'YOUR_MODEL_ID'
  const examples = useMemo(
    () => buildIntegrationCodeExamples(modelId),
    [modelId]
  )

  return (
    <PublicLayout showMainContainer={false}>
      <main className='mx-auto grid w-full max-w-6xl gap-8 px-4 pt-24 pb-16 md:grid-cols-[190px_minmax(0,1fr)] md:px-6'>
        <aside className='md:sticky md:top-20 md:h-fit'>
          <p className='text-muted-foreground mb-3 text-xs font-semibold tracking-wider uppercase'>
            {t('On this page')}
          </p>
          <nav aria-label={t('Documentation sections')} className='flex gap-2 overflow-x-auto pb-2 md:block md:space-y-1 md:overflow-visible md:pb-0'>
            {DOC_SECTIONS.map((section) => (
              <a className='text-muted-foreground hover:text-foreground block shrink-0 rounded-md px-2 py-1.5 text-xs transition-colors' href={`#${section.id}`} key={section.id}>
                {t(section.title)}
              </a>
            ))}
          </nav>
        </aside>

        <div className='min-w-0 space-y-12'>
          <header>
            <p className='text-primary text-sm font-semibold'>{MODELVIVID_BRAND.name}</p>
            <h1 className='mt-2 text-3xl font-bold tracking-tight sm:text-4xl'>{t('Codex integration guide')}</h1>
            <p className='text-muted-foreground mt-3 max-w-2xl text-sm leading-6 sm:text-base'>
              {t('Connect Codex, OpenAI-compatible SDKs, and Claude Code to one secure ModelVivid endpoint.')}
            </p>
          </header>

          <section id='quick-start' className='scroll-mt-24 space-y-3'>
            <h2 className='text-2xl font-semibold'>{t('Quick start')}</h2>
            <ol className='text-muted-foreground list-inside list-decimal space-y-2 text-sm leading-6'>
              <li>{t('Create an account and sign in to the ModelVivid console.')}</li>
              <li>{t('Create an API key and store it in a password manager; it is shown only when created.')}</li>
              <li>{t('Set the base URL and choose one of the enabled models below.')}</li>
            </ol>
          </section>

          <section id='models' className='scroll-mt-24 space-y-4'>
            <h2 className='text-2xl font-semibold'>{t('Available models')}</h2>
            <p className='text-muted-foreground text-sm'>{t('This list is read from the live ModelVivid catalog; prices and availability are not guessed.')}</p>
            <ModelList models={models} loading={pricing.isLoading} error={pricing.error} />
          </section>

          <section id='api-key' className='scroll-mt-24 space-y-3'>
            <h2 className='text-2xl font-semibold'>{t('API key')}</h2>
            <p className='text-muted-foreground text-sm leading-6'>{t('Send the key in the Authorization header. Never commit it to source control or expose it in browser code.')}</p>
            <CodeBlock value='Authorization: Bearer YOUR_MODELVIVID_API_KEY' label='HTTP header' />
          </section>

          <section id='base-url' className='scroll-mt-24 space-y-3'>
            <h2 className='text-2xl font-semibold'>{t('Base URL')}</h2>
            <p className='text-muted-foreground text-sm leading-6'>{t('Use this URL exactly once; SDKs append the endpoint path for you.')}</p>
            <CodeBlock value={examples.baseUrl} label='Base URL' />
          </section>

          <section id='codex-cli' className='scroll-mt-24 space-y-4'>
            <h2 className='text-2xl font-semibold'>{t('Codex CLI')}</h2>
            <CodeBlock value={examples.codexMacOS} label='macOS / Linux' />
            <CodeBlock value={examples.codexPowerShell} label='Windows PowerShell' />
          </section>

          <section id='openai-compatible' className='scroll-mt-24 space-y-4'>
            <h2 className='text-2xl font-semibold'>{t('OpenAI-compatible SDKs')}</h2>
            <CodeBlock value={examples.curl} label='curl' />
            <CodeBlock value={examples.python} label='Python' />
            <CodeBlock value={examples.node} label='Node.js' />
          </section>

          <section id='claude-code' className='scroll-mt-24 space-y-4'>
            <h2 className='text-2xl font-semibold'>{t('Claude Code')}</h2>
            <p className='text-muted-foreground text-sm leading-6'>{t('Configure Claude Code or CC Switch with the OpenAI-compatible endpoint and the same API key.')}</p>
            <CodeBlock value={examples.claude} label='Environment variables' />
          </section>

          <section id='first-request' className='scroll-mt-24 space-y-4'>
            <h2 className='text-2xl font-semibold'>{t('First request')}</h2>
            <CodeBlock value={examples.curl} label='Chat completion' />
            <p className='text-muted-foreground text-sm'>{t('A successful response contains choices[0].message.content. Keep the request ID when contacting support.')}</p>
          </section>

          <section id='troubleshooting' className='scroll-mt-24 space-y-4'>
            <h2 className='text-2xl font-semibold'>{t('Troubleshooting')}</h2>
            <dl className='space-y-4 text-sm leading-6'>
              <div><dt className='font-medium'>401</dt><dd className='text-muted-foreground'>{t('Check the Bearer key, its status, and the account that owns it.')}</dd></div>
              <div><dt className='font-medium'>404</dt><dd className='text-muted-foreground'>{t('Use the canonical base URL and do not duplicate /v1.')}</dd></div>
              <div><dt className='font-medium'>429</dt><dd className='text-muted-foreground'>{t('Slow down retries and check the configured account or channel limits.')}</dd></div>
              <div><dt className='font-medium'>{t('Hanging request')}</dt><dd className='text-muted-foreground'>{t('Set a client timeout, enable streaming only when needed, and retry with the request ID.')}</dd></div>
            </dl>
          </section>

          <a className='text-primary inline-flex items-center gap-1 text-sm hover:underline' href={MODELVIVID_BRAND.homePath}>
            {t('Back to ModelVivid home')} <ExternalLink className='size-3.5' />
          </a>
        </div>
      </main>
    </PublicLayout>
  )
}
