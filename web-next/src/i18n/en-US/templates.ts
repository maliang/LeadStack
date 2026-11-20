/**
 * Templates page (/templates) — en-US strings.
 * Kept in its own file so parallel edits to common.ts / deploy.ts do not conflict;
 * keys use the templates.* prefix, plus the new nav.templates label.
 */
const templates: Record<string, string> = {
  'nav.templates': 'Templates',

  'templates.title': 'App templates',
  'templates.description': 'Maintain Compose, systemd and binary templates and reuse them when starting a release.',
  'templates.new': 'New template',
  'templates.count': '{count} template(s)',
  'templates.searchPlaceholder': 'Search name / kind',
  'templates.noPermissionTitle': 'No permission to view templates',
  'templates.noPermissionText':
    'This account is missing deploy.read, so the template list is hidden. Ask an administrator for access.',
  'templates.loadFailed': 'Failed to load templates',
  'templates.emptyTitle': 'No templates yet',
  'templates.emptyText': 'Create your first template and it becomes selectable when starting a release.',
  'templates.noMatchTitle': 'No matching templates',
  'templates.noMatchText': 'No template matches "{keyword}". Try another keyword or clear the search.',
  'templates.clearSearch': 'Clear search',
  'templates.tableCaption': 'App template list',
  'templates.readOnly': 'Read-only',
  'templates.created': 'Created template "{name}"',
  'templates.saved': 'Saved template "{name}"',
  'templates.deleted': 'Deleted template "{name}"',
  'templates.editNamed': 'Edit template {name}',
  'templates.deleteNamed': 'Delete template {name}',

  'templates.col.name': 'Name',
  'templates.col.kind': 'Kind',
  'templates.col.updatedAt': 'Updated',

  'templates.kind.compose': 'Compose',
  'templates.kind.systemd': 'systemd',
  'templates.kind.binary': 'Binary',

  'templates.form.createTitle': 'New template',
  'templates.form.editTitle': 'Edit template',
  'templates.form.description': 'Template content is used verbatim by the release pipeline — check indentation and placeholders.',
  'templates.form.name': 'Template name',
  'templates.form.namePlaceholder': 'e.g. shop-web-compose',
  'templates.form.nameRequired': 'Template name is required',
  'templates.form.nameTooLong': 'Template name must be 64 characters or fewer',
  'templates.form.kind': 'Template kind',
  'templates.form.content': 'Template content',
  'templates.form.contentRequired': 'Template content is required',
  'templates.form.contentHint': 'Supports {{.Var}} placeholders; rendering a preview runs no commands.',
  'templates.form.invalid': 'Fix the form errors first',

  'templates.delete.title': 'Delete template',
  'templates.delete.description': 'This cannot be undone. Confirm the template and its impact below.',
  'templates.delete.detailName': 'Template: {name}',
  'templates.delete.detailKind': 'Kind: {kind}',
  'templates.delete.detailImpact':
    'Release history that referenced this template is kept, but new releases can no longer select it.',
  'templates.delete.confirm': 'Delete template',
}

export default templates
