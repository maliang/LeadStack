/** 端口与网络地址的客户端校验（与后端 §49 规则保持一致，错误以 i18n key 返回，便于就近展示）。 */

export function parsePort(value: string): number | null {
  const trimmed = value.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const port = Number(trimmed)
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null
  return port
}

/** host:port；host 允许为空（等价于全部地址），端口 1-65535。 */
export function validateListenAddress(value: string): string | null {
  const trimmed = value.trim()
  if (trimmed === '') return 'network.error.listenRequired'
  const match = /^(?:\[[0-9a-fA-F:]+\]|[^\s:]*):(\d{1,5})$/.exec(trimmed)
  if (!match) return 'network.error.listenFormat'
  const port = Number(match[1])
  if (!Number.isInteger(port) || port < 1 || port > 65535) return 'network.error.listenPort'
  return null
}

/** 对外地址：http(s)://，且不能是通配地址。 */
export function validatePublicUrl(value: string): string | null {
  const trimmed = value.trim()
  if (trimmed === '') return null
  let url: URL
  try {
    url = new URL(trimmed)
  } catch {
    return 'network.error.publicUrlScheme'
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    return 'network.error.publicUrlScheme'
  }
  const hostname = url.hostname.replace(/^\[|\]$/g, '')
  if (hostname === '0.0.0.0' || hostname === '::' || hostname === '') {
    return 'network.error.publicUrlWildcard'
  }
  return null
}

/** 服务器主机地址：不能为空，不能带协议或端口。 */
export function validateServerHost(value: string): string | null {
  const trimmed = value.trim()
  if (trimmed === '') return 'server.edit.errorHostRequired'
  if (/\s/.test(trimmed)) return 'server.edit.errorHostWhitespace'
  if (trimmed.includes('://')) return 'server.edit.errorHostScheme'
  if (trimmed.includes('/') || trimmed.includes(':')) return 'server.edit.errorHostPort'
  return null
}
