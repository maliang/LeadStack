import { Button } from './Button'
import { ChannelTestSection } from './ChannelTestSection'
import { ChannelKindBadge } from './AlertBadges'
import { Modal } from './Modal'
import type { AlertChannel } from '../lib/api'
import { t } from '../i18n'

interface ChannelTestDialogProps {
  channel: AlertChannel | null
  onClose: () => void
  onResult?: (ok: boolean) => void
}

/** 列表页「测试发送」弹窗：真实成功/失败原因都展示在这个弹窗里。 */
export function ChannelTestDialog({ channel, onClose, onResult }: ChannelTestDialogProps) {
  return (
    <Modal
      open={channel !== null}
      onClose={onClose}
      title={t('s.0303')}
      description={channel ? t('s.0304', { '0': channel.name }) : undefined}
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t('s.0264')}
        </Button>
      }
    >
      {channel ? (
        <div className="space-y-3">
          <div className="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-300">
            <ChannelKindBadge kind={channel.kind} />
            <span className="truncate">{channel.name}</span>
            {!channel.enabled ? (
              <span className="rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-500 dark:bg-slate-800 dark:text-slate-400">
                {t('s.0232')}
              </span>
            ) : null}
          </div>
          <ChannelTestSection channelId={channel.id} onResult={onResult} />
          <p className="text-xs text-slate-400 dark:text-slate-500">
            {t('s.0305')}
          </p>
        </div>
      ) : null}
    </Modal>
  )
}
