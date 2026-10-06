import { Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { AuthOperationError } from '@/lib/secure-verification'

import { importGptGatewayChannelJSON } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'

export function GatewayChannelJSONDialog(props: {
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
  const [configJSON, setConfigJSON] = useState('')
  const [validation, setValidation] = useState('')

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setValidation('')
    try {
      const value: unknown = JSON.parse(configJSON)
      if (!value || typeof value !== 'object' || Array.isArray(value)) {
        throw new Error('invalid')
      }
    } catch {
      setValidation('Paste a valid CPA or Sub2API channel JSON object.')
      return
    }
    const submittedJSON = configJSON
    const result = await operation.execute(null, async (_proof, signal) => {
      if (!globalThis.crypto?.subtle) {
        throw new AuthOperationError(
          'This verification method is currently unavailable.'
        )
      }
      const digest = await globalThis.crypto.subtle.digest(
        'SHA-256',
        new TextEncoder().encode(submittedJSON)
      )
      signal.throwIfAborted()
      const configSHA256 = [...new Uint8Array(digest)]
        .map((byte) => byte.toString(16).padStart(2, '0'))
        .join('')
      const proof = await operation.verification.requestVerification({
        scope: 'gpt_account_gateway.channel_import',
        context: { config_sha256: configSHA256 },
      })
      signal.throwIfAborted()
      if (!proof) {
        return undefined
      }
      return importGptGatewayChannelJSON(
        submittedJSON,
        proof.proof_token,
        signal
      )
    })
    if (!result) return
    setConfigJSON('')
    props.onSaved()
    props.onClose()
  }

  return (
    <>
      <Dialog
        open={!operation.verification.isActive}
        onOpenChange={(open) => {
          if (!open && !operation.pending) props.onClose()
        }}
        title={t('Import CPA/Sub2API channel JSON')}
        description={t(
          'Create a relay channel directly from a CPA or Sub2API JSON object.'
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
              form='gpt-gateway-channel-json-form'
              disabled={operation.pending}
            >
              {operation.pending && <Loader2 className='animate-spin' />}
              {t('Create channel')}
            </Button>
          </>
        }
      >
        <form
          id='gpt-gateway-channel-json-form'
          onSubmit={submit}
          className='space-y-4'
        >
          {(validation || operation.error) && (
            <p role='alert' className='text-destructive text-sm'>
              {t(validation || operation.error)}
            </p>
          )}
          <p className='text-muted-foreground text-sm'>
            {t(
              'Use a relay configuration with provider, base_url, and api_key (or relay_key). Account OAuth credentials are not relay channel keys.'
            )}
          </p>
          <div className='space-y-2'>
            <Label htmlFor='gpt-gateway-channel-json'>{t('Channel JSON')}</Label>
            <Textarea
              id='gpt-gateway-channel-json'
              value={configJSON}
              onChange={(event) => setConfigJSON(event.target.value)}
              disabled={operation.pending}
              autoComplete='off'
              spellCheck={false}
              rows={12}
              placeholder='{"provider":"sub2api","base_url":"https://...","api_key":"...","models":["gpt-5"]}'
              aria-invalid={Boolean(validation)}
            />
          </div>
        </form>
      </Dialog>
      <SecureVerificationDialog {...operation.verification.dialogProps} />
    </>
  )
}
