import { Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'

import { importGptAccountOAuth } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import { accountGroupSchema } from '../lib/group-form'
import type { GptAccountGateway } from '../types'

interface OAuthImportDialogProps {
  defaultGroup?: string
  defaultModels?: string
  defaultGatewayId?: number
  accountGroup?: string
  accountModels?: string
  accountId?: number
  gateways?: GptAccountGateway[]
  onClose: () => void
  onSaved: () => void
}

function extractEmail(raw: string): string | null {
  try {
    const value: unknown = JSON.parse(raw)
    if (!value || typeof value !== 'object' || !('email' in value)) return null
    const email = value.email
    return typeof email === 'string' && email.trim()
      ? email.trim().toLowerCase()
      : null
  } catch {
    return null
  }
}

export function OAuthImportDialog(props: OAuthImportDialogProps) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
  const [oauthJSON, setOAuthJSON] = useState('')
  const [group, setGroup] = useState('')
  const [models, setModels] = useState('')
  const [gatewayId, setGatewayId] = useState(props.defaultGatewayId ?? 0)
  const [validation, setValidation] = useState('')

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setValidation('')
    const email = extractEmail(oauthJSON)
    if (!email) {
      setValidation(
        'Paste a valid Codex OAuth JSON object containing an email.'
      )
      return
    }
    const parsedGroup = accountGroupSchema.safeParse(group)
    if (!parsedGroup.success) {
      setValidation('Enter a valid group of up to 64 bytes.')
      return
    }
    const result = await operation.execute((signal) =>
      importGptAccountOAuth(
        props.accountId,
        {
          oauth_json: oauthJSON,
          ...(!props.accountId && parsedGroup.data
            ? { group: parsedGroup.data }
            : {}),
          ...(!props.accountId && models.trim()
            ? { models: models.trim() }
            : {}),
          ...(!props.accountId ? { gateway_id: gatewayId } : {}),
        },
        signal
      )
    )
    if (!result) return
    setOAuthJSON('')
    props.onSaved()
    props.onClose()
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !operation.pending) props.onClose()
      }}
      title={t(
        props.accountId ? 'Update Codex OAuth JSON' : 'Import Codex OAuth JSON'
      )}
      description={t(
        'Import a Codex OAuth JSON credential and create or update the matching GPT account channel.'
      )}
      footer={
        <>
          <Button
            variant='outline'
            onClick={props.onClose}
            disabled={operation.pending}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='gpt-oauth-import-form'
            disabled={operation.pending}
          >
            {operation.pending && <Loader2 className='animate-spin' />}
            {t('Import OAuth JSON')}
          </Button>
        </>
      }
    >
      <form id='gpt-oauth-import-form' onSubmit={submit} className='space-y-4'>
        {(validation || operation.error) && (
          <p role='alert' className='text-destructive text-sm'>
            {t(validation || operation.error)}
          </p>
        )}
        <p className='text-muted-foreground text-sm'>
          {t(
            'The credential is stored encrypted and never shown in the account list or audit log. Do not paste passwords or unrelated tokens.'
          )}
        </p>
        {!props.accountId && (
          <div className='space-y-2'>
            <Label htmlFor='gpt-oauth-import-gateway'>
              {t('Account destination')}
            </Label>
            <NativeSelect
              id='gpt-oauth-import-gateway'
              value={gatewayId}
              disabled={operation.pending}
              onChange={(event) => setGatewayId(Number(event.target.value))}
            >
              <NativeSelectOption value={0}>
                {t('Direct Codex channel')}
              </NativeSelectOption>
              {(props.gateways ?? []).map((gateway) => {
                let providerLabel = t('VM2API')
                if (gateway.provider === 'cpa') {
                  providerLabel = 'CPA'
                } else if (gateway.provider === 'sub2api') {
                  providerLabel = 'Sub2API'
                }
                return (
                  <NativeSelectOption key={gateway.id} value={gateway.id}>
                    {gateway.name} · {providerLabel}
                  </NativeSelectOption>
                )
              })}
            </NativeSelect>
          </div>
        )}
        <div className='space-y-2'>
          <Label htmlFor='gpt-oauth-import-group'>{t('Group')}</Label>
          <Input
            id='gpt-oauth-import-group'
            value={props.accountId ? (props.accountGroup ?? '') : group}
            readOnly={Boolean(props.accountId)}
            disabled={operation.pending}
            onChange={(event) => setGroup(event.target.value)}
            placeholder={t('Use global default: {{group}}', {
              group: props.defaultGroup ?? 'default',
            })}
          />
          <p className='text-muted-foreground text-sm'>
            {props.accountId
              ? t('Manage routing settings in the associated channel.')
              : t('Existing matching accounts keep their current group.')}
          </p>
        </div>
        <div className='space-y-2'>
          <Label htmlFor='gpt-oauth-import-models'>{t('Models')}</Label>
          <Input
            id='gpt-oauth-import-models'
            value={props.accountId ? (props.accountModels ?? '') : models}
            readOnly={Boolean(props.accountId)}
            disabled={operation.pending}
            onChange={(event) => setModels(event.target.value)}
            placeholder={t('Use global default models: {{models}}', {
              models: props.defaultModels ?? 'gpt-5.6',
            })}
          />
          <p className='text-muted-foreground text-sm'>
            {props.accountId
              ? t('Manage routing settings in the associated channel.')
              : t(
                  'Separate model names with commas. Leave blank to use the global default models.'
                )}
          </p>
        </div>
        <div className='space-y-2'>
          <Label htmlFor='gpt-oauth-import-json'>{t('OAuth JSON')}</Label>
          <Textarea
            id='gpt-oauth-import-json'
            value={oauthJSON}
            onChange={(event) => {
              setOAuthJSON(event.target.value)
              setValidation('')
            }}
            rows={10}
            disabled={operation.pending}
            autoComplete='off'
            spellCheck={false}
            aria-invalid={Boolean(validation)}
          />
        </div>
      </form>
    </Dialog>
  )
}
