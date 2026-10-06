import { Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'

import { importGptAccountOAuth } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'

interface OAuthImportDialogProps {
  accountId?: number
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
    const result = props.accountId
      ? await operation.execute(
          {
            scope: 'gpt_account.oauth_import.update',
            context: { account_id: props.accountId },
          },
          (proof, signal) =>
            importGptAccountOAuth(
              props.accountId,
              { oauth_json: oauthJSON },
              proof,
              signal
            )
        )
      : await operation.execute(
          {
            scope: 'gpt_account.oauth_import.create',
            context: { email },
          },
          (proof, signal) =>
            importGptAccountOAuth(undefined, { oauth_json: oauthJSON }, proof, signal)
        )
    if (!result) return
    setOAuthJSON('')
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
        <form
          id='gpt-oauth-import-form'
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
              'The credential is stored encrypted and never shown in the account list or audit log. Do not paste passwords or unrelated tokens.'
            )}
          </p>
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
      <SecureVerificationDialog {...operation.verification.dialogProps} />
    </>
  )
}
