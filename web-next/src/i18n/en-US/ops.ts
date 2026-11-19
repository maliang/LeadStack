/**
 * Operations pages copy (en-US): /tokens, /audit, /backups, /settings, /credentials, /groups.
 *
 * Only new strings for these six pages live here; reusable keys (common.* / nav.* / users.* /
 * role.* / tokens.* / audit.* / groups.* / settings.* / network.* / credentials.*) are never
 * duplicated, so check-i18n-keys.mjs keeps passing.
 */
const ops: Record<string, string> = {
  'ops.common.createdAt': 'Created',
  'ops.common.unknown': 'Unknown',

  'ops.tokens.expiry': 'Expiry',
  'ops.tokens.expiryHint': 'The token stops working after expiry; rotate it yourself if you pick "never".',
  'ops.tokens.createdToast': 'Created token {name}',
  'ops.tokens.revokeTitle': 'Revoke token',
  'ops.tokens.revokeConfirm': 'Revoke token',
  'ops.tokens.copied': 'Token copied',
  'ops.tokens.copyFailed': 'Copy failed — select the token text and copy it manually',

  'ops.audit.expand': 'Show details',
  'ops.audit.collapse': 'Hide details',
  'ops.audit.readOnly': 'Audit log is read-only: entries cannot be edited or deleted.',
  'ops.audit.clearFilters': 'Clear filters',

  'ops.backups.title': 'Backups',
  'ops.backups.description':
    'Encrypted online backups of the control-plane data directory; the list shows archive metadata only.',
  'ops.backups.create': 'Back up now',
  'ops.backups.createHint': 'Runs online without stopping the service',
  'ops.backups.includeArtifacts': 'Include uploaded artifacts',
  'ops.backups.includeArtifactsHint':
    'Off by default: artifacts can be large — enable only when migrating them together.',
  'ops.backups.created': 'Backup created: {name}',
  'ops.backups.createFailed': 'Failed to create backup',
  'ops.backups.name': 'Archive',
  'ops.backups.size': 'Size',
  'ops.backups.schemaVersion': 'Schema version',
  'ops.backups.includesArtifacts': 'Artifacts',
  'ops.backups.artifactsYes': 'With artifacts',
  'ops.backups.artifactsNo': 'Without artifacts',
  'ops.backups.sha256': 'SHA-256',
  'ops.backups.download': 'Download',
  'ops.backups.downloadStarted': 'Downloading {name}',
  'ops.backups.downloadFailed': 'Failed to download backup',
  'ops.backups.deleteTitle': 'Delete backup',
  'ops.backups.deleteImpact': 'This permanently deletes archive "{name}"; it cannot be undone.',
  'ops.backups.deleteConfirm': 'Delete backup',
  'ops.backups.deleted': 'Deleted backup "{name}"',
  'ops.backups.deleteFailed': 'Failed to delete backup',
  'ops.backups.loadFailed': 'Failed to load backups',
  'ops.backups.emptyTitle': 'No backups yet',
  'ops.backups.emptyDescription':
    'Create the first backup: a consistent database snapshot is encrypted into an archive you can restore from.',
  'ops.backups.count': '{count} backups',
  'ops.backups.securityNote':
    'Archives are encrypted; the passphrase is not stored inside them — keep it somewhere separate.',

  'ops.settings.accountHint': 'Signed-in account and running version.',
  'ops.settings.versionUnavailable': 'Version unavailable',
  'ops.settings.links': 'Shortcuts',
  'ops.settings.networkNeedsPermission': 'Network settings require the "system manage" permission.',
  'ops.settings.securityNote': 'After changing the password this session stays valid; other sessions must sign in again.',
  'ops.settings.loadFailed': 'Failed to load settings',

  'ops.credentials.pageTitle': 'Credentials',
  'ops.credentials.pageDescription':
    'Login credentials for agentless SSH access; keys and passwords are stored encrypted and never echoed back.',
  'ops.credentials.create': 'New credential',
  'ops.credentials.createHint': 'Passwords and keys exist only in the create request and cannot be read afterwards.',
  'ops.credentials.kind': 'Auth type',
  'ops.credentials.kind.password': 'Password',
  'ops.credentials.kind.key': 'Private key',
  'ops.credentials.kindHint.password': 'Username + password',
  'ops.credentials.kindHint.key': 'Username + private key (passphrase optional)',
  'ops.credentials.name': 'Credential name',
  'ops.credentials.namePlaceholder': 'e.g. web-prod-root',
  'ops.credentials.username': 'Username',
  'ops.credentials.password': 'Password',
  'ops.credentials.privateKey': 'Private key',
  'ops.credentials.privateKeyPlaceholder': 'Paste a PEM private key (-----BEGIN OPENSSH PRIVATE KEY----- …)',
  'ops.credentials.passphrase': 'Key passphrase (optional)',
  'ops.credentials.fingerprint': 'Fingerprint',
  'ops.credentials.serverCount': 'Used by servers',
  'ops.credentials.created': 'Created credential "{name}"',
  'ops.credentials.createFailed': 'Failed to create credential',
  'ops.credentials.deleteTitle': 'Delete credential',
  'ops.credentials.deleteImpact': 'Servers using this credential can no longer connect; this cannot be undone.',
  'ops.credentials.deleteConfirm': 'Delete credential',
  'ops.credentials.deleted': 'Deleted credential "{name}"',
  'ops.credentials.deleteFailed': 'Failed to delete credential',
  'ops.credentials.loadFailed': 'Failed to load credentials',
  'ops.credentials.emptyTitle': 'No credentials yet',
  'ops.credentials.emptyDescription': 'Create a login credential before connecting SSH servers.',
  'ops.credentials.count': '{count} credentials',
  'ops.credentials.securityNote':
    'Passwords and keys appear only in the create request; the list shows type and fingerprint.',
  'ops.credentials.errorName': 'Enter a credential name',
  'ops.credentials.errorUsername': 'Enter a username',
  'ops.credentials.errorPassword': 'Enter a password',
  'ops.credentials.errorKey': 'Paste the private key',
  'ops.credentials.noFingerprint': 'Not provided',
  'ops.credentials.noServers': 'Unused',

  'ops.groups.count': '{count} groups',
  'ops.groups.membersSelected': '{count} selected',
  'ops.groups.deleteConfirm': 'Delete group',
  'ops.groups.membersEmpty': 'No servers available to join a group',
}

export default ops
