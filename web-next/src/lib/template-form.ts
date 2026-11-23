import type { CreateTemplatePayload, DeployTemplate, TemplateKind } from './api'

/**
 * 应用模板纯函数：表单值、校验、payload 构造、类型标签、搜索。
 *
 * 页面与弹窗只调用这里，不在模板里各写一套「类型 → 文案」映射，
 * 也不在事件处理里散落校验规则；这样同一份口径能被冒烟脚本直接断言。
 * 所有错误文案返回 i18n 键，由页面用 t() 渲染。
 */

/** 后端支持的三种模板类型（顺序即下拉展示顺序）。 */
export const TEMPLATE_KINDS: readonly TemplateKind[] = ['compose', 'systemd', 'binary']

/** 模板表单值（全部是字符串 / 受控枚举，方便直接绑 input）。 */
export interface TemplateFormValues {
  name: string
  kind: TemplateKind
  content: string
}

export type TemplateFormErrors = Partial<Record<keyof TemplateFormValues, string>>

export const TEMPLATE_NAME_MAX = 64

/** 内置示例模板：新增时一键填充，降低「不知道 compose/systemd 怎么写」的门槛。 */
export interface TemplateSample {
  key: string
  nameKey: string
  kind: TemplateKind
  content: string
}

export const TEMPLATE_SAMPLES: readonly TemplateSample[] = [
  {
    key: 'compose-nginx',
    nameKey: 'templates.sample.composeNginx',
    kind: 'compose',
    content: `services:
  web:
    image: nginx:1.27-alpine
    restart: unless-stopped
    ports:
      - "8080:80"
    volumes:
      - {{ROOT}}/html:/usr/share/nginx/html:ro
`,
  },
  {
    key: 'systemd-binary',
    nameKey: 'templates.sample.systemdBinary',
    kind: 'systemd',
    content: `[Unit]
Description={{APP_SLUG}}
After=network.target

[Service]
Type=simple
WorkingDirectory={{ROOT}}/current
ExecStart={{ROOT}}/current/bin/{{APP_SLUG}}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`,
  },
  {
    key: 'script-binary',
    nameKey: 'templates.sample.scriptBinary',
    kind: 'binary',
    content: `#!/usr/bin/env sh
set -eu
mkdir -p "{{ROOT}}/releases/{{RELEASE_ID}}"
curl -fsSL "{{ARTIFACT_URL}}" -o /tmp/{{APP_SLUG}}.tar.gz
tar -xzf /tmp/{{APP_SLUG}}.tar.gz -C "{{ROOT}}/releases/{{RELEASE_ID}}"
ln -sfn "{{ROOT}}/releases/{{RELEASE_ID}}" "{{ROOT}}/current"
systemctl restart "{{APP_SLUG}}"
`,
  },
]

export function emptyTemplateForm(): TemplateFormValues {
  return { name: '', kind: 'compose', content: '' }
}

/** 后端可能返回历史/未知 kind：收敛到三选一，未知按 binary（与 normalizeTemplate 的兜底一致）。 */
export function normalizeTemplateKind(value: string | null | undefined): TemplateKind {
  const raw = (value ?? '').trim().toLowerCase()
  return (TEMPLATE_KINDS as readonly string[]).includes(raw) ? (raw as TemplateKind) : 'binary'
}

export function templateFormFromItem(item: DeployTemplate): TemplateFormValues {
  return {
    name: item.name ?? '',
    kind: normalizeTemplateKind(item.kind),
    content: item.content ?? '',
  }
}

/** 类型 → i18n 键；归一化后必然命中三种之一，界面不会出现空标签。 */
export function templateKindLabelKey(kind: string | null | undefined): string {
  return `templates.kind.${normalizeTemplateKind(kind)}`
}

/** 必填校验：name 去掉首尾空白后非空且 ≤64，content 不能只有空白。 */
export function validateTemplateForm(values: TemplateFormValues): TemplateFormErrors {
  const errors: TemplateFormErrors = {}
  const name = values.name.trim()
  if (name === '') errors.name = 'templates.form.nameRequired'
  else if (name.length > TEMPLATE_NAME_MAX) errors.name = 'templates.form.nameTooLong'
  if (values.content.trim() === '') errors.content = 'templates.form.contentRequired'
  return errors
}

export function templatePayloadFromForm(values: TemplateFormValues): CreateTemplatePayload {
  return {
    name: values.name.trim(),
    kind: normalizeTemplateKind(values.kind),
    // 模板内容保留原始缩进与开头，只裁掉末尾空白（避免每次保存多出空行）。
    content: values.content.replace(/\s+$/, ''),
  }
}

/** 编辑用 PATCH 体：只带真正变化的字段（后端三字段都可选，至少一个）。 */
export function templatePatchFromForm(
  item: DeployTemplate,
  values: TemplateFormValues,
): Partial<CreateTemplatePayload> {
  const next = templatePayloadFromForm(values)
  const patch: Partial<CreateTemplatePayload> = {}
  if (next.name !== (item.name ?? '')) patch.name = next.name
  if (next.kind !== normalizeTemplateKind(item.kind)) patch.kind = next.kind
  if (next.content !== (item.content ?? '')) patch.content = next.content
  return patch
}

/** 搜索文本：名称 + 类型代码 + 类型本地化标签（标签由调用方注入，避免 lib 里复制文案）。 */export function templateSearchText(
  item: DeployTemplate,
  labelOf: (item: DeployTemplate) => string = (target) => String(target.kind ?? ''),
): string {
  return [item.name ?? '', item.kind ?? '', labelOf(item)].join(' ').toLowerCase()
}

export function filterTemplates(
  list: readonly DeployTemplate[],
  keyword: string,
  labelOf?: (item: DeployTemplate) => string,
): DeployTemplate[] {
  const needle = keyword.trim().toLowerCase()
  if (needle === '') return [...list]
  return list.filter((item) => templateSearchText(item, labelOf).includes(needle))
}
