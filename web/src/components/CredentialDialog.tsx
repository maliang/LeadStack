import { useEffect, useRef, useState, type FormEvent } from 'react'
import { api, type Credential, type CredentialKind } from '../lib/api'
import { errorMessage } from '../lib/errors'
import { Button } from './Button'
import { Modal } from './Modal'
import { useToast } from './Toast'
import { t } from '../i18n'

interface CredentialDialogProps {
  open: boolean
  onClose: () => void
  onCreated?: (credential: Credential) => void
}

const inputClass =
  'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 dark:focus:border-indigo-400'

const labelClass = 'block text-sm font-medium text-slate-700 dark:text-slate-200'

/**
 * 新建凭证弹窗。
 * 安全约定：密码/私钥只以受控输入的形式存在，提交成功后立刻清空；
 * 界面上任何位置都不会回显已保存的密钥内容（接口也只返回元数据）。
 */
export function CredentialDialog({ open, onClose, onCreated }: CredentialDialogProps) {
  const { toast } = useToast()
  const [kind, setKind] = useState<CredentialKind>('password')
  const [name, setName] = useState('')
  const [username, setUsername] = useState('root')
  const [password, setPassword] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [keyFileName, setKeyFileName] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const reset = () => {
    setKind('password')
    setName('')
    setUsername('root')
    setPassword('')
    setPrivateKey('')
    setPassphrase('')
    setKeyFileName('')
    setShowKey(false)
    setSaving(false)
    setError(null)
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  useEffect(() => {
    if (open) reset()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const handleKeyFile = async (file: File | undefined) => {
    if (!file) return
    try {
      const text = await file.text()
      setPrivateKey(text)
      setKeyFileName(file.name)
    } catch {
      setError(t('s.0390'))
    }
  }

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!name.trim()) {
      setError(t('s.0391'))
      return
    }
    if (!username.trim()) {
      setError(t('s.0392'))
      return
    }
    if (kind === 'password' && !password) {
      setError(t('s.0393'))
      return
    }
    if (kind === 'key' && !privateKey.trim()) {
      setError(t('s.0394'))
      return
    }

    setSaving(true)
    setError(null)
    try {
      const created = await api.createCredential(
        kind === 'password'
          ? { name: name.trim(), kind: 'password', username: username.trim(), password }
          : {
              name: name.trim(),
              kind: 'key',
              username: username.trim(),
              private_key: privateKey,
              passphrase: passphrase || undefined,
            },
      )
      // 提交成功即清空内存中的密钥，不保留、不回显
      reset()
      toast(t('s.0395', { '0': created.name }), 'success')
      onCreated?.(created)
      onClose()
    } catch (caught) {
      setError(errorMessage(caught, t('s.0396')))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        reset()
        onClose()
      }}
      title={t('s.0070')}
      description={t('s.0397')}
      footer={
        <>
          <Button
            variant="secondary"
            disabled={saving}
            onClick={() => {
              reset()
              onClose()
            }}
          >
            {t('s.0024')}
          </Button>
          <Button type="submit" form="credential-form" loading={saving}>
            {t('s.0398')}
          </Button>
        </>
      }
    >
      <form id="credential-form" onSubmit={handleSubmit} className="space-y-4">
        <div>
          <span className={labelClass}>{t('s.0399')}</span>
          <div className="mt-1.5 grid grid-cols-2 gap-2">
            {(
              [
                { value: 'password', label: t('s.0073'), hint: t('s.0400') },
                { value: 'key', label: t('s.0072'), hint: t('s.0401') },
              ] as const
            ).map((option) => (
              <button
                key={option.value}
                type="button"
                onClick={() => setKind(option.value)}
                className={
                  'rounded-lg border px-3 py-2 text-left transition-colors ' +
                  (kind === option.value
                    ? 'border-indigo-500 bg-indigo-50/70 dark:border-indigo-400 dark:bg-indigo-500/10'
                    : 'border-slate-200 hover:border-slate-300 dark:border-slate-700 dark:hover:border-slate-600')
                }
              >
                <span className="block text-sm font-medium text-slate-800 dark:text-slate-100">
                  {option.label}
                </span>
                <span className="mt-0.5 block text-xs text-slate-500 dark:text-slate-400">
                  {option.hint}
                </span>
              </button>
            ))}
          </div>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor="credential-name" className={labelClass}>
              {t('s.0402')}
            </label>
            <input
              id="credential-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={64}
              placeholder={t('s.0403')}
              className={inputClass + ' mt-1.5'}
            />
          </div>
          <div>
            <label htmlFor="credential-username" className={labelClass}>
              {t('s.0112')}
            </label>
            <input
              id="credential-username"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              autoComplete="off"
              placeholder="root"
              className={inputClass + ' mt-1.5'}
            />
          </div>
        </div>

        {kind === 'password' ? (
          <div>
            <label htmlFor="credential-password" className={labelClass}>
              {t('s.0073')}
            </label>
            <input
              id="credential-password"
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="new-password"
              placeholder={t('s.0404')}
              className={inputClass + ' mt-1.5'}
            />
          </div>
        ) : (
          <div className="space-y-3">
            <div>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <label htmlFor="credential-key" className={labelClass}>
                  {t('s.0405')}
                </label>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => setShowKey((shown) => !shown)}
                    className="text-xs font-medium text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200"
                  >
                    {showKey ? t('s.0406') : t('s.0407')}
                  </button>
                  <button
                    type="button"
                    onClick={() => fileInputRef.current?.click()}
                    className="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
                  >
                    {t('s.0408')}
                  </button>
                </div>
              </div>
              <textarea
                id="credential-key"
                value={privateKey}
                onChange={(event) => setPrivateKey(event.target.value)}
                rows={5}
                spellCheck={false}
                placeholder={'-----BEGIN OPENSSH PRIVATE KEY-----'}
                className={
                  inputClass +
                  ' mt-1.5 resize-y font-mono text-xs leading-relaxed' +
                  (showKey ? '' : ' [-webkit-text-security:disc]')
                }
              />
              <input
                ref={fileInputRef}
                type="file"
                accept=".pem,.key,.txt,.ppk,text/plain"
                className="hidden"
                onChange={(event) => void handleKeyFile(event.target.files?.[0])}
              />
              {keyFileName ? (
                <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">
                  {t('s.0409')}{keyFileName}{t('s.0410')}
                </p>
              ) : null}
            </div>
            <div>
              <label htmlFor="credential-passphrase" className={labelClass}>
                {t('s.0411')} <span className="font-normal text-slate-400">{t('s.0040')}</span>
              </label>
              <input
                id="credential-passphrase"
                type="password"
                value={passphrase}
                onChange={(event) => setPassphrase(event.target.value)}
                autoComplete="new-password"
                placeholder={t('s.0412')}
                className={inputClass + ' mt-1.5'}
              />
            </div>
          </div>
        )}

        <p className="text-xs text-slate-400 dark:text-slate-500">
          {t('s.0413')}
        </p>

        {error ? (
          <p className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">
            {error}
          </p>
        ) : null}
      </form>
    </Modal>
  )
}
