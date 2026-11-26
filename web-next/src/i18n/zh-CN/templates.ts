/**
 * 应用模板页（/templates）的 zh-CN 文案。
 * 独立文件避免与 common.ts / deploy.ts 的并行改动冲突；键统一 templates.* 前缀，
 * 仅导航标签沿用 nav.templates（同为新增键，不覆盖既有键）。
 */
const templates: Record<string, string> = {
  'nav.templates': '部署预设',

  'templates.title': '部署预设',
  'templates.description': '维护可复用的部署方案；新应用可以从预设套用，也可以另存为预设。',
  'templates.new': '新建预设',
  'templates.count': '共 {count} 个预设',
  'templates.searchPlaceholder': '搜索模板名 / 类型',
  'templates.noPermissionTitle': '没有模板查看权限',
  'templates.noPermissionText': '当前账号缺少 deploy.read 权限，模板列表不可见。请联系管理员分配权限。',
  'templates.loadFailed': '模板列表加载失败',
  'templates.emptyTitle': '还没有模板',
  'templates.emptyText': '新建第一个模板后，发起发布时即可直接选用。',
  'templates.noMatchTitle': '没有匹配的模板',
  'templates.noMatchText': '没有匹配「{keyword}」的模板，换个关键词或清空搜索条件。',
  'templates.clearSearch': '清空搜索',
  'templates.tableCaption': '应用模板列表',
  'templates.readOnly': '只读',
  'templates.created': '已创建模板「{name}」',
  'templates.saved': '已保存模板「{name}」',
  'templates.deleted': '已删除模板「{name}」',
  'templates.editNamed': '编辑模板 {name}',
  'templates.deleteNamed': '删除模板 {name}',

  'templates.col.name': '名称',
  'templates.col.kind': '类型',
  'templates.col.updatedAt': '更新时间',

  'templates.kind.compose': 'Compose',
  'templates.kind.systemd': 'systemd',
  'templates.kind.binary': '二进制',

  'templates.form.createTitle': '新建模板',
  'templates.form.editTitle': '编辑模板',
  'templates.form.description': '模板内容会原样用于发布编排，请确认缩进与占位符。',
  'templates.form.name': '模板名称',
  'templates.form.namePlaceholder': '例如 shop-web-compose',
  'templates.form.nameRequired': '请填写模板名称',
  'templates.form.nameTooLong': '模板名称不超过 64 个字符',
  'templates.form.kind': '模板类型',
  'templates.form.content': '模板内容',
  'templates.form.contentRequired': '请填写模板内容',
  'templates.form.placeholders': '可用占位符（点击插入）',
  'templates.kind.composeHint': 'docker compose 文件：适合容器化服务，发布时渲染后执行 docker compose up -d。',
  'templates.kind.systemdHint': 'systemd unit 文件：适合裸二进制常驻服务，发布时写到 /etc/systemd/system 后 reload/restart。',
  'templates.kind.binaryHint': '发布脚本：适合自有流程（解包、切 symlink、重启服务），脚本在目标机执行。',
  'templates.form.samples': '示例模板',
  'templates.sample.composeNginx': 'Compose · Nginx',
  'templates.sample.systemdBinary': 'Systemd · 裸二进制',
  'templates.sample.scriptBinary': '脚本 · 解包并重启',
  'templates.form.contentHint': '支持 {{APP_SLUG}} 这类占位符；渲染预览不会执行任何命令。',
  'templates.form.invalid': '请先修正表单中的错误',

  'templates.delete.title': '删除模板',
  'templates.delete.description': '此操作不可撤销，请确认要删除的模板与影响范围。',
  'templates.delete.detailName': '模板：{name}',
  'templates.delete.detailKind': '类型：{kind}',
  'templates.delete.detailImpact': '引用该模板的发布历史不会删除，但新建发布将无法选择它。',
  'templates.delete.confirm': '删除模板',
}

export default templates
