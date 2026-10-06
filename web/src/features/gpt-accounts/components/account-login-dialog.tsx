import { ExternalLink, Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { formatTimestamp } from '@/lib/format'

import { authorizeGptAccount, finishGptAccountAuthorization } from '../api'
import { useAccountOperation } from '../hooks/use-account-operation'
import type { GptAccount, GptAccountAuthorization } from '../types'

interface AccountLoginDialogProps {
  account: GptAccount
  onClose: () => void
  onSaved: () => void
}

export function AccountLoginDialog(props: AccountLoginDialogProps) {
  const { t } = useTranslation()
  const operation = useAccountOperation()
  const [authorization, setAuthorization] =
    useState<GptAccountAuthorization | null>(null)
  const [redirectUrl, setRedirectUrl] = useState('')
  const [validation, setValidation] = useState('')

  const start = async () => {
    setValidation('')
    setAuthorization(null)
    setRedirectUrl('')
    const result = await operation.execute(
      authorizeGptAccount.bind(null, props.account.id)
    )
    if (!result) return
    try {
      const url = new URL(result.authorization_url)
      if (
        url.protocol !== 'https:' ||
        url.hostname !== 'auth.openai.com' ||
        url.username ||
        url.password ||
        result.expires_at * 1000 <= Date.now()
      ) {
        throw new Error('invalid authorization')
      }
      setAuthorization(result)
      setRedirectUrl('')
    } catch {
      setValidation(
        'The authorization link is invalid or expired. Generate a new link.'
      )
    }
  }
  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setValidation('')
    if (!authorization || authorization.expires_at * 1000 <= Date.now()) {
      setAuthorization(null)
      setRedirectUrl('')
      setValidation(
        'The authorization link is invalid or expired. Generate a new link.'
      )
      return
    }
    try {
      const url = new URL(redirectUrl.trim())
      const expectedState = new URL(
        authorization.authorization_url
      ).searchParams.get('state')
      if (
        url.origin !== 'http://localhost:1455' ||
        url.pathname !== '/auth/callback' ||
        url.username ||
        url.password ||
        url.hash ||
        !expectedState ||
        url.searchParams.getAll('state').length !== 1 ||
        url.searchParams.get('state') !== expectedState ||
        url.searchParams.getAll('code').length !== 1 ||
        !url.searchParams.get('code')
      ) {
        throw new Error('invalid callback')
      }
    } catch {
      setValidation(
        'Paste the complete localhost callback URL containing code and state.'
      )
      return
    }
    const callbackUrl = redirectUrl.trim()
    setRedirectUrl('')
    setAuthorization(null)
    const result = await operation.execute((signal) =>
      finishGptAccountAuthorization(props.account.id, callbackUrl, signal)
    )
    if (!result) return
    props.onSaved()
    props.onClose()
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !operation.pending) props.onClose()
      }}
      title={t('Complete browser sign-in')}
      description={props.account.email}
      footer={
        <Button
          variant='outline'
          disabled={operation.pending}
          onClick={props.onClose}
        >
          {t('Close')}
        </Button>
      }
    >
      <div className='space-y-4'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Open the official sign-in page, complete any email verification, CAPTCHA or consent, then paste the final localhost URL here. Do not share it.'
          )}
        </p>
        {(validation || operation.error) && (
          <p role='alert' className='text-destructive text-sm'>
            {t(validation || operation.error)}
          </p>
        )}
        <Button onClick={start} disabled={operation.pending}>
          {operation.pending && <Loader2 className='animate-spin' />}
          {t('Generate sign-in link')}
        </Button>
        {authorization && (
          <>
            <div className='flex flex-wrap items-center gap-2'>
              <Button
                nativeButton={false}
                role='link'
                render={
                  <a
                    href={authorization.authorization_url}
                    target='_blank'
                    rel='noopener noreferrer'
                    referrerPolicy='no-referrer'
                  />
                }
              >
                <ExternalLink />
                {t('Open official sign-in')}
              </Button>
              <CopyButton
                value={authorization.authorization_url}
                tooltip={t('Copy sign-in link')}
              />
            </div>
            <p className='text-muted-foreground text-sm'>
              {t('Expires at {{time}}', {
                time: formatTimestamp(authorization.expires_at),
              })}
            </p>
            <form onSubmit={submit} className='space-y-3'>
              <Label htmlFor='gpt-account-callback'>{t('Callback URL')}</Label>
              <Input
                id='gpt-account-callback'
                type='password'
                value={redirectUrl}
                onChange={(event) => setRedirectUrl(event.target.value)}
                autoComplete='off'
                spellCheck={false}
                disabled={operation.pending}
                aria-invalid={Boolean(validation)}
              />
              <Button
                type='submit'
                disabled={operation.pending || !redirectUrl.trim()}
              >
                {t('Complete authorization')}
              </Button>
            </form>
          </>
        )}
      </div>
    </Dialog>
  )
}
