/**
 * Server batch operations (en-US).
 * Keep the key set identical to zh-CN/serversBatch.ts.
 */
const serversBatch: Record<string, string> = {
  'serversBatch.actions': 'Batch actions',
  'serversBatch.selectAll': 'Select all filtered results ({count})',
  'serversBatch.selectOne': 'Select {name}',
  'serversBatch.selected': '{count} selected',
  'serversBatch.clearSelection': 'Clear selection',
  'serversBatch.confirm': 'Run',
  'serversBatch.cancel': 'Cancel',
  'serversBatch.submitting': 'Running…',

  'serversBatch.action.testConnection': 'Test connection',
  'serversBatch.action.refreshHostInfo': 'Refresh host info',
  'serversBatch.action.addTags': 'Add tags',
  'serversBatch.action.setGroup': 'Set group',
  'serversBatch.action.delete': 'Delete',

  'serversBatch.dialog.testConnection.title': 'Test connections in bulk',
  'serversBatch.dialog.testConnection.desc': 'SSH will test the {count} selected servers one by one; this can take a while for large selections.',
  'serversBatch.dialog.testConnection.confirm': 'Start test',
  'serversBatch.dialog.refreshHostInfo.title': 'Refresh host info in bulk',
  'serversBatch.dialog.refreshHostInfo.desc': 'SSH will collect the OS and hardware details of the {count} selected servers again.',
  'serversBatch.dialog.refreshHostInfo.confirm': 'Start refresh',
  'serversBatch.dialog.addTags.title': 'Add tags in bulk',
  'serversBatch.dialog.addTags.desc': 'Append tags to the {count} selected servers; existing tags are not duplicated.',
  'serversBatch.dialog.addTags.label': 'Tags',
  'serversBatch.dialog.addTags.placeholder': 'Separate multiple tags with commas or new lines',
  'serversBatch.dialog.addTags.hint': 'At most {max} tags, each up to {len} characters.',
  'serversBatch.dialog.addTags.confirm': 'Add tags',
  'serversBatch.dialog.setGroup.title': 'Set group in bulk',
  'serversBatch.dialog.setGroup.desc': 'Move the {count} selected servers into the same group.',
  'serversBatch.dialog.setGroup.label': 'Group',
  'serversBatch.dialog.setGroup.custom': 'Enter a new group…',
  'serversBatch.dialog.setGroup.newLabel': 'New group name',
  'serversBatch.dialog.setGroup.placeholder': 'Enter a new group name',
  'serversBatch.dialog.setGroup.confirm': 'Set group',
  'serversBatch.dialog.delete.title': 'Delete {count} servers',
  'serversBatch.dialog.delete.desc': 'This runs immediately and cannot be undone.',
  'serversBatch.dialog.delete.warning': '{count} servers will be permanently deleted together with their metrics and events.',
  'serversBatch.dialog.delete.confirm': 'Permanently delete {count}',

  'serversBatch.dialog.targetsTitle': 'Affected servers',

  'serversBatch.result.title': 'Batch action results',
  'serversBatch.result.subtitle': '{action} · {count} servers',
  'serversBatch.result.summary': '{ok} succeeded, {failed} failed',
  'serversBatch.result.successTitle': 'Succeeded ({count})',
  'serversBatch.result.failedTitle': 'Failed ({count})',
  'serversBatch.result.okBadge': 'OK',
  'serversBatch.result.noError': 'No error message returned',
  'serversBatch.result.retry': 'Retry',
  'serversBatch.result.retrying': 'Retrying…',
  'serversBatch.result.retried': 'Retried',

  'serversBatch.error.requestFailed': 'Batch action failed',
  'serversBatch.error.retryFailed': 'Retry failed',
  'serversBatch.error.noSelection': 'Select at least one server first',
  'serversBatch.error.tooMany': 'At most {max} servers per batch',
  'serversBatch.error.tagsEmpty': 'Enter at least one tag',
  'serversBatch.error.tagsTooMany': 'Enter at most {max} tags',
  'serversBatch.error.tagTooLong': 'Tag "{tag}" exceeds {len} characters',
  'serversBatch.error.groupEmpty': 'Choose or enter a group',
  'serversBatch.error.groupTooLong': 'Group name must be at most {len} characters',
}

export default serversBatch
