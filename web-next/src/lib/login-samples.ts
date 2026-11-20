/**
 * 「一键登录」内置脚本示例。
 *
 * 这些脚本既是界面上的预置按钮，也是 `scripts/verify_servers.sh` 第 17 节比对的
 * 「第三份」来源——README.md 与 docs/ARCHITECTURE.md 里给用户复制的示例必须与
 * 这里逐字节相同，否则「文档里粘出来的」与「界面点出来的」行为会不一致。
 *
 * 约定（后端 POST /servers/{id}/login）：脚本在**目标机**上执行，stdout 输出 JSON，
 * 键为 url / terminal / output 之一。脚本输出会写进任务日志，所以脚本里不得含密码。
 */
export interface LoginScriptSample {
  key: string
  labelKey: string
  script: string
}

export const LOGIN_SCRIPT_SAMPLES: LoginScriptSample[] = [
  {
    key: 'baota',
    labelKey: 'server.login.sampleBaota',
    script: `#!/usr/bin/env bash
# 宝塔面板 v14 起可用「bt 14」打印面板地址（含安全入口路径）；只提取其中的完整 URL。
# 注意：不要把含初始密码的整段输出写进脚本，脚本输出会被记录到任务日志里。
PREFIX=$(bt 14 2>/dev/null || /etc/init.d/bt 14 2>/dev/null)
# bt 14 的输出常带颜色控制码，不清洗会把转义序列吃进 URL；部分系统输出是 CRLF，\\r 也会被 grep 吃进 URL 尾部。
PREFIX=$(printf '%s' "$PREFIX" | tr -d '\\r' | sed -E $'s/\\x1B\\[[0-9;]*[A-Za-z]//g')
PANEL_URL=$(printf '%s' "$PREFIX" | grep -oE 'https?://[^ ]+' | head -n 1)
if [ -n "$PANEL_URL" ]; then
  printf '{"url":"%s"}' "$PANEL_URL"
else
  printf '{"output":"没有从 bt 14 的输出里找到面板地址，请到「面板设置」确认面板端口与安全入口"}'
fi`,
  },
  {
    key: 'onepanel',
    labelKey: 'server.login.sampleOnePanel',
    script: `#!/usr/bin/env bash
# 1Panel 只有 API Key 鉴权，没有免密网页登录；这里输出一条 SSH 命令交给网页终端。
# 把 host/port 换成这台服务器的实际值（脚本里不要写密码）。
printf '{"terminal":"ssh -p %s %s@%s"}' '22' 'root' '1.2.3.4'`,
  },
]
