/**
 * Dashboard domain: overview page title, KPIs, attention list, checks/alerts summary and recent releases (en-US).
 * Keys stay as flat dotted paths so they merge exactly like zh-CN/index.ts; add new keys to both locales together.
 */
const dashboard: Record<string, string> = {
  'dashboard.title': 'Overview',
  'dashboard.version': 'Version {version}',
  'dashboard.refresh': 'Refresh',
  'dashboard.retry': 'Retry',
  'dashboard.loading': 'Loading',
  'dashboard.valueEmpty': '—',
  'dashboard.partialTitle': 'Some data could not be refreshed',
  'dashboard.footnote': 'Refreshes every 10 seconds; polling pauses while the page is in the background.',

  'dashboard.kpi.title': 'Key metrics',
  'dashboard.kpi.total': 'Servers',
  'dashboard.kpi.online': 'Online',
  'dashboard.kpi.offline': 'Offline / unknown',
  'dashboard.kpi.offlineHint': 'Needs attention',
  'dashboard.kpi.uptime': 'Up for {uptime}',
  'dashboard.kpi.onlineRate': 'Online rate {rate}',
  'dashboard.kpi.loading': 'Loading server overview…',
  'dashboard.kpi.loadFailed': 'Could not load the server overview',
  'dashboard.kpi.noServers': 'No servers yet — add a target host to populate the overview.',

  'dashboard.attention.title': 'Needs attention',
  'dashboard.attention.subtitle': 'Servers not in the online state, most recently updated first',
  'dashboard.attention.loadFailed': 'Could not load the server list',
  'dashboard.attention.staleTitle': 'Server list could not be refreshed; showing the last result',
  'dashboard.attention.noServers': 'No servers yet.',
  'dashboard.attention.allOnline': 'All servers are online — nothing needs attention.',
  'dashboard.attention.more': '{count} more not shown',

  'dashboard.checks.title': 'Checks summary',
  'dashboard.checks.subtitle': 'Latest result for every check',
  'dashboard.checks.viewAll': 'View checks',
  'dashboard.checks.total': 'Checks',
  'dashboard.checks.up': 'Up',
  'dashboard.checks.down': 'Down',
  'dashboard.checks.unknown': 'Unknown',
  'dashboard.checks.empty': 'No checks yet — add your first check on the Checks page.',
  'dashboard.checks.staleTitle': 'Checks summary could not be refreshed; showing the last result',
  'dashboard.checks.loadFailed': 'Could not load the checks summary',

  'dashboard.alerts.title': 'Alerts summary',
  'dashboard.alerts.subtitle': 'Active alerts and recoveries in the last 24 hours',
  'dashboard.alerts.viewAll': 'View alerts',
  'dashboard.alerts.firing': 'Firing',
  'dashboard.alerts.acked': 'Acknowledged',
  'dashboard.alerts.resolved24h': 'Resolved 24h',
  'dashboard.alerts.severity': 'Critical/Warning/Info',
  'dashboard.alerts.empty': 'No alerts recorded.',
  'dashboard.alerts.staleTitle': 'Alerts summary could not be refreshed; showing the last result',
  'dashboard.alerts.loadFailed': 'Could not load the alerts summary',

  'dashboard.releases.title': 'Recent releases',
  'dashboard.releases.subtitle': 'The last 5 releases and their run results',
  'dashboard.releases.viewAll': 'View releases',
  'dashboard.releases.unnamed': 'Unnamed app',
  'dashboard.releases.runs': 'Succeeded {success} · Failed {failed} · Running {active}',
  'dashboard.releases.empty': 'No releases yet.',
  'dashboard.releases.scopedEmpty': 'No releases in the scope you can see yet.',
  'dashboard.releases.noPermission': 'This account cannot view releases.',
  'dashboard.releases.staleTitle': 'Recent releases could not be refreshed; showing the last result',
  'dashboard.releases.loadFailed': 'Could not load recent releases',
}

export default dashboard
