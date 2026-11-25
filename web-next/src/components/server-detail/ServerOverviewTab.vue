<script setup lang="ts">
import { computed, ref } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import { useCan } from '@/composables/useCan'
import { api } from '@/lib/api'
import { errorMessage } from '@/lib/errors'
import { writeClipboard } from '@/lib/clipboard'
import { serverStatusLabelKey, serverStatusTone } from '@/composables/useServers'
import { useI18n } from '@/i18n'
import type { AgentUpgradeResult, HostInfo, Server, TestConnectionResult } from '@/lib/api'
import {
  formatBytes,
  formatDateTime,
  formatOs,
  formatRelativeTime,
  formatUptime,
  serverAddress,
} from '@/lib/format'
import {
  agentUpgradeFieldError,
  normalizeAgentUpgradeDraft,
  type AgentUpgradeDraft,
} from '@/lib/server-metrics'

/**
 * 「概览」标签：这台机器是谁、通不通、跑什么版本，以及主机信息 / 测试连接 / Agent 升级。
 *
 * 纯展示：数据与异步状态全部由页面持有（本页是唯一 owner），这里只渲染 props
 * 并派发事件。面板入口只在服务器绑定了 panel_credential_id 时出现（未绑定时后端
 * 返回 409，按钮是噪音）；写操作按钮只在 canWrite 时渲染，缺权限时按钮数为 0、
 * 页面也不会为此发任何请求。
 */
const props = defineProps<{
  server: Server
  panelBusy: boolean
  panelError: string | null
  /** server.read：指标曲线只读 */
  canRead: boolean
  /** server.write：刷新主机信息 / 测试连接 / Agent 升级 */
  canWrite: boolean
  hostBusy: boolean
  hostError: string | null
  hostNotice: string | null
  connection: TestConnectionResult | null
  connectionBusy: boolean
  connectionError: string | null
  upgradeBusy: boolean
  upgradeResult: AgentUpgradeResult | null
  upgradeError: string | null
}>()

const emit = defineEmits<{
  (event: 'open-panel'): void
  (event: 'refresh-host'): void
  (event: 'test-connection'): void
  (event: 'agent-upgrade', payload: AgentUpgradeDraft): void
}>()

const { t } = useI18n()
const { can } = useCan()
const canEnroll = computed(() => can('enroll.manage'))

const hasPanel = computed(() => (props.server.panel_credential_id ?? '') !== '')
const lastSeen = computed(() =>
  props.server.last_seen_at ? formatRelativeTime(props.server.last_seen_at) : t('s.0543'),
)
const modeLabel = computed(() => (props.server.mode === 'agent' ? t('s.1095') : t('s.0036')))
const createdAt = computed(() =>
  props.server.created_at ? formatDateTime(props.server.created_at) : t('common.na'),
)
const needsOnboarding = computed(() => props.server.status !== 'online')
const onboardingCommand = ref('')
const onboardingBusy = ref(false)
const onboardingError = ref<string | null>(null)
const onboardingCopied = ref(false)

async function generateOnboarding(): Promise<void> {
  if (onboardingBusy.value) return
  onboardingBusy.value = true
  onboardingError.value = null
  try {
    const token = await api.createEnrollToken({
      note: `server ${props.server.name}`,
      ttl_s: 3600,
      server_id: props.server.id,
    })
    if (!token.token) throw new Error(t('server.onboard.tokenMissing'))
    const script = await api.installScript({ token: token.token, server_url: '' })
    onboardingCommand.value = script.command
  } catch (caught) {
    onboardingError.value = errorMessage(caught, t('server.onboard.generateFailed'))
  } finally {
    onboardingBusy.value = false
  }
}

async function copyOnboarding(): Promise<void> {
  onboardingCopied.value = await writeClipboard(onboardingCommand.value)
  if (onboardingCopied.value) setTimeout(() => (onboardingCopied.value = false), 1600)
}

const groupLabels = computed(() => {
  if (props.server.group_names.length > 0) return props.server.group_names
  return props.server.group ? [props.server.group] : []
})

/* 主机信息：一行一个字段，值统一走 format 层 */
interface HostRow {
  key: string
  labelKey: string
  value: string
  mono?: boolean
}

const hostRows = computed<HostRow[]>(() => {
  const info: HostInfo | null = props.server.host_info
  if (!info) return []
  const na = t('common.na')
  const diskParts = [
    info.disk_used_bytes !== null ? formatBytes(info.disk_used_bytes) : null,
    info.disk_total_bytes !== null ? formatBytes(info.disk_total_bytes) : null,
  ].filter((part): part is string => part !== null)
  const osParts = [info.os, info.os_version].filter((part): part is string => !!part)
  return [
    { key: 'hostname', labelKey: 'servers.detail.host.hostname', value: info.hostname || na, mono: true },
    { key: 'os', labelKey: 'servers.detail.host.os', value: osParts.join(' ') || na },
    { key: 'kernel', labelKey: 'servers.detail.host.kernel', value: info.kernel || na, mono: true },
    { key: 'arch', labelKey: 'servers.detail.host.arch', value: info.arch || na, mono: true },
    { key: 'cpu', labelKey: 'servers.detail.host.cpuModel', value: info.cpu_model || na },
    {
      key: 'cores',
      labelKey: 'servers.detail.host.cpuCores',
      value: info.cpu_cores === null ? na : String(info.cpu_cores),
      mono: true,
    },
    {
      key: 'mem',
      labelKey: 'servers.detail.host.memTotal',
      value: info.mem_total_bytes === null ? na : formatBytes(info.mem_total_bytes),
      mono: true,
    },
    {
      key: 'disk',
      labelKey: 'servers.detail.host.disk',
      value: diskParts.length > 0 ? diskParts.join(' / ') : na,
      mono: true,
    },
    {
      key: 'uptime',
      labelKey: 'servers.detail.host.uptime',
      value: info.uptime_s === null ? na : formatUptime(info.uptime_s),
    },
    { key: 'timezone', labelKey: 'servers.detail.host.timezone', value: info.timezone || na, mono: true },
    {
      key: 'docker',
      labelKey: 'servers.detail.host.docker',
      value: info.docker ? info.docker_version || t('servers.detail.host.dockerEnabled') : t('servers.detail.host.dockerDisabled'),
    },
    {
      key: 'ip',
      labelKey: 'servers.detail.host.ipAddrs',
      value: info.ip_addrs.length > 0 ? info.ip_addrs.join(', ') : na,
      mono: true,
    },
    {
      key: 'collected',
      labelKey: 'servers.detail.host.collectedAt',
      value: info.collected_at ? formatDateTime(info.collected_at) : na,
    },
  ]
})

/* Agent 升级表单：草稿只在组件内，校验与归一化由 lib 纯函数负责 */
const upgradeVersion = ref('')
const upgradeUrl = ref('')
const upgradeSha256 = ref('')
const upgradeFormError = ref<string | null>(null)

function submitUpgrade(): void {
  const draft: AgentUpgradeDraft = {
    version: upgradeVersion.value,
    url: upgradeUrl.value,
    sha256: upgradeSha256.value,
  }
  const fieldError = agentUpgradeFieldError(draft)
  if (fieldError !== null) {
    upgradeFormError.value = t(`servers.detail.agentUpgrade.error.${fieldError}`)
    return
  }
  upgradeFormError.value = null
  emit('agent-upgrade', normalizeAgentUpgradeDraft(draft))
}
</script>

<template>
  <div class="ls-detail__stack">
    <div v-if="server.last_error" class="ls-banner ls-banner--warning" role="status">
      <AppIcon name="alert-triangle" size="1rem" />
      <div class="ls-banner__body">
        <p class="ls-banner__title">{{ t('s.1092') }}</p>
        <p class="ls-banner__text">{{ server.last_error }}</p>
      </div>
    </div>

    <section v-if="needsOnboarding" id="server-onboarding" class="ls-card" :aria-label="t('server.onboard.title')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="info" size="1rem" />
            {{ t('server.onboard.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('server.onboard.hint') }}</p>
        </div>
        <span class="ls-chip ls-chip--warning">{{ t('server.onboard.offline') }}</span>
      </header>
      <ol class="ls-onboard__steps">
        <li>{{ server.mode === 'agent' ? t('server.onboard.agentStep1') : t('server.onboard.sshStep1') }}</li>
        <li>{{ server.mode === 'agent' ? t('server.onboard.agentStep2') : t('server.onboard.sshStep2') }}</li>
        <li>{{ t('server.onboard.step3') }}</li>
      </ol>
      <p v-if="onboardingError" class="ls-inline-error" role="alert">{{ onboardingError }}</p>
      <template v-if="server.mode === 'agent' && canEnroll">
        <div class="ls-card__actions">
          <button id="server-onboard-generate" class="ls-btn ls-btn--primary" type="button" :disabled="onboardingBusy" :aria-busy="onboardingBusy" @click="generateOnboarding">
            <span v-if="onboardingBusy" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="key-round" size="0.875rem" />
            {{ onboardingBusy ? t('server.onboard.generating') : t('server.onboard.generate') }}
          </button>
          <button v-if="onboardingCommand" id="server-onboard-copy" class="ls-btn" type="button" @click="copyOnboarding">
            <AppIcon name="copy" size="0.875rem" />
            {{ onboardingCopied ? t('common.copied') : t('server.onboard.copy') }}
          </button>
        </div>
        <pre v-if="onboardingCommand" id="server-onboard-command" class="ls-server-form__command">{{ onboardingCommand }}</pre>
        <p v-if="onboardingCommand" class="ls-field__hint">{{ t('server.onboard.commandHint') }}</p>
      </template>
      <p v-else-if="server.mode === 'agent'" class="ls-field__hint">{{ t('server.onboard.needEnrollPermission') }}</p>
      <div v-else class="ls-card__actions">
        <button class="ls-btn ls-btn--primary" type="button" @click="emit('test-connection')">{{ t('server.onboard.testSsh') }}</button>
      </div>
    </section>

    <section class="ls-card" :aria-label="t('s.1093')">
      <header class="ls-card__head">
        <h2 class="ls-card__title">{{ t('s.1093') }}</h2>
        <button
          v-if="hasPanel"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="panelBusy"
          @click="emit('open-panel')"
        >
          <span v-if="panelBusy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="archive" size="0.875rem" />
          {{ panelBusy ? t('server.panel.opening') : t('server.panel.open') }}
        </button>
      </header>

      <p v-if="panelError" class="ls-inline-error" role="alert">{{ panelError }}</p>

      <dl class="ls-kv">
        <div class="ls-kv__row">
          <dt>{{ t('s.0066') }}</dt>
          <dd class="ls-kv__value--mono" :title="serverAddress(server)">{{ serverAddress(server) }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0533') }}</dt>
          <dd>
            <span class="ls-srv-status" :class="`ls-srv-status--${serverStatusTone(server.status)}`">
              <span class="ls-srv-status__dot" aria-hidden="true" />
              {{ t(serverStatusLabelKey(server.status)) }}
            </span>
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1094') }}</dt>
          <dd>{{ modeLabel }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0538') }}</dt>
          <dd :title="lastSeen">{{ lastSeen }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1096') }}</dt>
          <dd :title="formatOs(server.os, server.arch)">{{ formatOs(server.os, server.arch) }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1097') }}</dt>
          <dd class="ls-kv__value--mono" :title="server.kernel ?? ''">
            {{ server.kernel || t('common.na') }}
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1098') }}</dt>
          <dd class="ls-kv__value--mono" :title="server.agent_version ?? ''">
            {{ server.agent_version || t('common.na') }}
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0062') }}</dt>
          <dd>
            <span v-if="groupLabels.length > 0" class="ls-chip-group">
              <span v-for="group in groupLabels" :key="group" class="ls-chip" :title="group">
                {{ group }}
              </span>
            </span>
            <span v-else>{{ t('common.na') }}</span>
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0075') }}</dt>
          <dd>
            <span v-if="server.tags.length > 0" class="ls-chip-group">
              <span v-for="tag in server.tags" :key="tag" class="ls-chip ls-chip--muted" :title="`#${tag}`">
                #{{ tag }}
              </span>
            </span>
            <span v-else>{{ t('common.na') }}</span>
          </dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.0271') }}</dt>
          <dd>{{ createdAt }}</dd>
        </div>
        <div class="ls-kv__row">
          <dt>{{ t('s.1099') }}</dt>
          <dd class="ls-kv__value--mono" :title="server.id">{{ server.id }}</dd>
        </div>
      </dl>
    </section>

    <!-- 主机信息 + 刷新按钮（server.write） -->
    <section class="ls-card" :aria-label="t('servers.detail.host.title')">
      <header class="ls-card__head">
        <div class="ls-card__heading">
          <h2 class="ls-card__title">
            <AppIcon name="server" size="1rem" />
            {{ t('servers.detail.host.title') }}
          </h2>
          <p class="ls-card__subtitle">{{ t('servers.detail.host.hint') }}</p>
        </div>
        <button
          v-if="canWrite"
          class="ls-btn ls-btn--small"
          type="button"
          :disabled="hostBusy"
          @click="emit('refresh-host')"
        >
          <span v-if="hostBusy" class="ls-spinner" aria-hidden="true" />
          <AppIcon v-else name="refresh" size="0.875rem" />
          {{ hostBusy ? t('servers.detail.host.refreshing') : t('servers.detail.host.refresh') }}
        </button>
      </header>

      <p v-if="hostNotice" class="ls-ok-hint" role="status">{{ hostNotice }}</p>
      <p v-if="hostError" class="ls-inline-error" role="alert">{{ hostError }}</p>

      <dl v-if="hostRows.length > 0" class="ls-kv">
        <div v-for="row in hostRows" :key="row.key" class="ls-kv__row">
          <dt>{{ t(row.labelKey) }}</dt>
          <dd :class="{ 'ls-kv__value--mono': row.mono }" :title="row.value">{{ row.value }}</dd>
        </div>
      </dl>
      <div v-else class="ls-card__note">
        <p>{{ t('server.overview.hostEmpty') }}</p>
        <p>{{ t('server.overview.hostEmptyHint') }}</p>
      </div>
    </section>

    <div class="ls-overview__panes">
      <!-- 测试连接（server.write）：ok=false 时原样显示后端 error -->
      <section class="ls-card" :aria-label="t('servers.detail.testConnection.title')">
        <header class="ls-card__head">
          <div class="ls-card__heading">
            <h2 class="ls-card__title">
              <AppIcon name="key" size="1rem" />
              {{ t('servers.detail.testConnection.title') }}
            </h2>
            <p class="ls-card__subtitle">{{ t('servers.detail.testConnection.hint') }}</p>
          </div>
          <button
            v-if="canWrite"
            class="ls-btn ls-btn--small"
            type="button"
            :disabled="connectionBusy"
            @click="emit('test-connection')"
          >
            <span v-if="connectionBusy" class="ls-spinner" aria-hidden="true" />
            <AppIcon v-else name="pulse" size="0.875rem" />
            {{ connectionBusy ? t('servers.detail.testConnection.running') : t('servers.detail.testConnection.run') }}
          </button>
        </header>

        <p v-if="connectionError" class="ls-inline-error" role="alert">{{ connectionError }}</p>

        <div v-if="connection" class="ls-conn">
          <p v-if="connection.ok" class="ls-ok-hint" role="status">
            <AppIcon name="shield-check" size="0.875rem" />
            {{ t('servers.detail.testConnection.ok') }}
          </p>
          <p v-else class="ls-inline-error" role="alert">{{ connection.error || t('common.na') }}</p>
          <dl class="ls-kv">
            <div class="ls-kv__row">
              <dt>{{ t('servers.detail.testConnection.latency') }}</dt>
              <dd class="ls-kv__value--mono">
                <template v-if="connection.latency_ms !== null">{{ connection.latency_ms }} ms</template>
                <template v-else>{{ t('common.na') }}</template>
              </dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('servers.detail.testConnection.banner') }}</dt>
              <dd class="ls-kv__value--mono" :title="connection.ssh_banner ?? ''">
                {{ connection.ssh_banner || t('common.na') }}
              </dd>
            </div>
            <div class="ls-kv__row">
              <dt>{{ t('servers.detail.testConnection.fingerprint') }}</dt>
              <dd class="ls-kv__value--mono" :title="connection.host_key_fingerprint ?? ''">
                {{ connection.host_key_fingerprint || t('common.na') }}
              </dd>
            </div>
          </dl>
        </div>
        <p v-else class="ls-card__note">{{ t('servers.detail.testConnection.empty') }}</p>
      </section>

      <!-- Agent 升级（server.write）：url 必须是 agent 归档；sha256 前端先校验 -->
      <section v-if="canWrite" class="ls-card" :aria-label="t('servers.detail.agentUpgrade.title')">
        <header class="ls-card__head">
          <div class="ls-card__heading">
            <h2 class="ls-card__title">
              <AppIcon name="archive" size="1rem" />
              {{ t('servers.detail.agentUpgrade.title') }}
            </h2>
            <p class="ls-card__subtitle">{{ t('servers.detail.agentUpgrade.hint') }}</p>
          </div>
        </header>

        <form class="ls-form" novalidate @submit.prevent="submitUpgrade">
          <label class="ls-field" for="server-agent-upgrade-version">
            <span class="ls-field__label">{{ t('servers.detail.agentUpgrade.version') }}</span>
            <input
              id="server-agent-upgrade-version"
              v-model="upgradeVersion"
              class="ls-input"
              type="text"
              autocomplete="off"
              :placeholder="t('servers.detail.agentUpgrade.versionPlaceholder')"
              :disabled="upgradeBusy"
            />
          </label>
          <label class="ls-field" for="server-agent-upgrade-url">
            <span class="ls-field__label">{{ t('servers.detail.agentUpgrade.url') }}</span>
            <input
              id="server-agent-upgrade-url"
              v-model="upgradeUrl"
              class="ls-input"
              type="url"
              autocomplete="off"
              spellcheck="false"
              :placeholder="t('servers.detail.agentUpgrade.urlPlaceholder')"
              :disabled="upgradeBusy"
            />
          </label>
          <label class="ls-field" for="server-agent-upgrade-sha256">
            <span class="ls-field__label">{{ t('servers.detail.agentUpgrade.sha256') }}</span>
            <input
              id="server-agent-upgrade-sha256"
              v-model="upgradeSha256"
              class="ls-input ls-metrics__monoInput"
              type="text"
              autocomplete="off"
              spellcheck="false"
              maxlength="64"
              :placeholder="t('servers.detail.agentUpgrade.sha256Placeholder')"
              :disabled="upgradeBusy"
            />
          </label>

          <p v-if="upgradeFormError" class="ls-field__error" role="alert">{{ upgradeFormError }}</p>
          <p v-if="upgradeError" class="ls-inline-error" role="alert">{{ upgradeError }}</p>

          <div v-if="upgradeResult" class="ls-upgrade__result" role="status">
            <p :class="upgradeResult.ok ? 'ls-ok-hint' : 'ls-inline-error'">
              {{ upgradeResult.ok ? t('servers.detail.agentUpgrade.queued') : upgradeResult.error || t('common.na') }}
            </p>
            <p v-if="upgradeResult.ok" class="ls-card__note">
              {{ t('servers.detail.agentUpgrade.queuedHint') }}
            </p>
          </div>

          <div class="ls-form__actions">
            <button
              class="ls-btn ls-btn--small ls-btn--primary"
              type="submit"
              :disabled="upgradeBusy"
            >
              <span v-if="upgradeBusy" class="ls-spinner" aria-hidden="true" />
              {{ upgradeBusy ? t('servers.detail.agentUpgrade.submitting') : t('servers.detail.agentUpgrade.submit') }}
            </button>
          </div>
        </form>
      </section>
    </div>
  </div>
</template>
