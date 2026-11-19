/**
 * 服务器批量操作（zh-CN）。
 * 键保持扁平点号路径；新增键请同步 en-US/serversBatch.ts。
 */
const serversBatch: Record<string, string> = {
  'serversBatch.actions': '批量操作',
  'serversBatch.selectAll': '全选当前筛选结果（{count} 台）',
  'serversBatch.selectOne': '选择 {name}',
  'serversBatch.selected': '已选 {count} 台',
  'serversBatch.clearSelection': '取消选择',
  'serversBatch.confirm': '确认执行',
  'serversBatch.cancel': '取消',
  'serversBatch.submitting': '正在执行…',

  'serversBatch.action.testConnection': '测试连接',
  'serversBatch.action.refreshHostInfo': '刷新主机信息',
  'serversBatch.action.addTags': '添加标签',
  'serversBatch.action.setGroup': '设置分组',
  'serversBatch.action.delete': '删除',

  'serversBatch.dialog.testConnection.title': '批量测试连接',
  'serversBatch.dialog.testConnection.desc': '将通过 SSH 逐台测试选中的 {count} 台服务器，数量多时耗时较长。',
  'serversBatch.dialog.testConnection.confirm': '开始测试',
  'serversBatch.dialog.refreshHostInfo.title': '批量刷新主机信息',
  'serversBatch.dialog.refreshHostInfo.desc': '将通过 SSH 重新采集选中的 {count} 台服务器的系统与硬件信息。',
  'serversBatch.dialog.refreshHostInfo.confirm': '开始刷新',
  'serversBatch.dialog.addTags.title': '批量添加标签',
  'serversBatch.dialog.addTags.desc': '为选中的 {count} 台服务器追加标签，已存在的标签不会重复。',
  'serversBatch.dialog.addTags.label': '标签',
  'serversBatch.dialog.addTags.placeholder': '多个标签用逗号或换行分隔',
  'serversBatch.dialog.addTags.hint': '最多 {max} 个标签，单个标签最长 {len} 个字符。',
  'serversBatch.dialog.addTags.confirm': '添加标签',
  'serversBatch.dialog.setGroup.title': '批量设置分组',
  'serversBatch.dialog.setGroup.desc': '将选中的 {count} 台服务器移动到同一分组。',
  'serversBatch.dialog.setGroup.label': '分组',
  'serversBatch.dialog.setGroup.custom': '输入新分组…',
  'serversBatch.dialog.setGroup.newLabel': '新分组名称',
  'serversBatch.dialog.setGroup.placeholder': '输入新分组名称',
  'serversBatch.dialog.setGroup.confirm': '设置分组',
  'serversBatch.dialog.delete.title': '删除 {count} 台服务器',
  'serversBatch.dialog.delete.desc': '该操作会立即执行，且无法撤销。',
  'serversBatch.dialog.delete.warning': '将永久删除 {count} 台服务器及其指标/事件。',
  'serversBatch.dialog.delete.confirm': '永久删除 {count} 台',

  'serversBatch.dialog.targetsTitle': '受影响的服务器',

  'serversBatch.result.title': '批量操作结果',
  'serversBatch.result.subtitle': '{action} · 共 {count} 台',
  'serversBatch.result.summary': '成功 {ok} 台，失败 {failed} 台',
  'serversBatch.result.successTitle': '成功（{count}）',
  'serversBatch.result.failedTitle': '失败（{count}）',
  'serversBatch.result.okBadge': '成功',
  'serversBatch.result.noError': '未返回错误信息',
  'serversBatch.result.retry': '重试',
  'serversBatch.result.retrying': '重试中…',
  'serversBatch.result.retried': '已重试',

  'serversBatch.error.requestFailed': '批量操作失败',
  'serversBatch.error.retryFailed': '重试失败',
  'serversBatch.error.noSelection': '请先选择服务器',
  'serversBatch.error.tooMany': '一次最多操作 {max} 台服务器',
  'serversBatch.error.tagsEmpty': '请至少填写一个标签',
  'serversBatch.error.tagsTooMany': '最多填写 {max} 个标签',
  'serversBatch.error.tagTooLong': '标签「{tag}」超过 {len} 个字符',
  'serversBatch.error.groupEmpty': '请选择或输入分组',
  'serversBatch.error.groupTooLong': '分组名称最长 {len} 个字符',
}

export default serversBatch
