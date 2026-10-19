import { Button } from './Button'
import { ChannelTestSection } from './ChannelTestSection'
import { ChannelKindBadge } from './AlertBadges'
import { Modal } from './Modal'
import type { AlertChannel } from '../lib/api'

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
      title="测试发送"
      description={channel ? `渠道：${channel.name}` : undefined}
      footer={
        <Button variant="secondary" onClick={onClose}>
          关闭
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
                已停用
              </span>
            ) : null}
          </div>
          <ChannelTestSection channelId={channel.id} onResult={onResult} />
          <p className="text-xs text-slate-400 dark:text-slate-500">
            测试失败常见原因：URL/Token 错误、机器人未加签或签名不匹配、被企业网络策略拦截、SMTP 端口或加密方式不对。
          </p>
        </div>
      ) : null}
    </Modal>
  )
}
