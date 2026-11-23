<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { useCan } from '@/composables/useCan'
import { useI18n } from '@/i18n'
import { api, type Credential, type ImportServersResult } from '@/lib/api'
import { errorMessage } from '@/lib/errors'

/**
 * 批量导入主机页（挂载面：AppShell 默认 slot → .ls-main__inner）。
 *
 * 契约来自旧 web/src/pages/ImportPage.tsx：POST /servers/import，
 * payload {text, default_port, credential_id, group?, tags[], mode}。
 * 本页在粘贴文本之外增加本地文件选择：File.text() 读入文本域后走同一套解析与预览，
 * 不新增任何后端端点；单次上限与后端一致（500 行）。
 * 解析、状态映射均为纯函数；凭证与网络配置各自有加载/失败/重试三态。
 */
const { t } = useI18n()
const { can } = useCan()

const canImport = computed(() => can('server.write'))

const MAX_LINES = 500

type PreviewNote = 'ok' | 'duplicate' | 'invalid'

interface PreviewRow {
  key: string
  host: string
  port: number
  note: PreviewNote
}

const credentials = ref<Credential[]>([])
const credentialState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const credentialError = ref<string | null>(null)

const text = ref('')
const defaultPort = ref('22')
const credentialId = ref('')
const group = ref('')
const tags = ref('')
const mode = ref<'ssh' | 'agent'>('ssh')

const fileName = ref('')
const fileLines = ref(0)
const fileError = ref<string | null>(null)

const submitting = ref(false)
const error = ref<string | null>(null)
const notice = ref<string | null>(null)
const result = ref<ImportServersResult | null>(null)

const parsed = computed<PreviewRow[]>(() => parseHostText(text.value, Number(defaultPort.value) || 22))
const validCount = computed(() => parsed.value.filter((row) => row.note === 'ok').length)

function parseHostText(input: string, fallbackPort: number): PreviewRow[] {
  const rows: PreviewRow[] = []
  const seen = new Set<string>()
  const lines = input.split(/\r?\n/)
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].trim()
    if (!line || line.startsWith('#')) continue
    const match = /^([^\s:]+)(?::(\d{1,5}))?$/.exec(line)
    if (!match) {
      rows.push({ key: `${index}-${line}`, host: line, port: fallbackPort, note: 'invalid' })
      continue
    }
    const host = match[1]
    const port = match[2] ? Number(match[2]) : fallbackPort
    const identity = `${host}:${port}`
    if (seen.has(identity)) {
      rows.push({ key: `${index}-${identity}`, host, port, note: 'duplicate' })
      continue
    }
    seen.add(identity)
    rows.push({ key: `${index}-${identity}`, host, port, note: 'ok' })
  }
  return rows
}

function previewStatusMeta(note: PreviewNote): { labelKey: string; className: string } {
  if (note === 'duplicate') return { labelKey: 'import.duplicate', className: 'ls-importpage__status--warn' }
  if (note === 'invalid') return { labelKey: 'import.invalid', className: 'ls-importpage__status--error' }
  return { labelKey: 'import.ok', className: 'ls-importpage__status--ok' }
}

function countLines(input: string): number {
  return input.split(/\r?\n/).filter((line) => line.trim() && !line.trim().startsWith('#')).length
}

async function loadCredentials(): Promise<void> {
  credentialState.value = 'loading'
  credentialError.value = null
  try {
    credentials.value = await api.credentials()
    credentialState.value = 'ready'
  } catch (err) {
    credentialError.value = errorMessage(err, t('hostimport.loadFailed'))
    credentialState.value = 'error'
  }
}

async function loadNetwork(): Promise<void> {
  try {
    const network = await api.network()
    defaultPort.value = String(network.default_ssh_port || 22)
  } catch {
    // 与旧实现一致：网络配置读取失败时回退 22，不阻断导入表单。
    defaultPort.value = '22'
  }
}

async function onFileChange(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  fileError.value = null
  if (!file) return
  try {
    const content = await file.text()
    if (!content.trim()) {
      fileError.value = t('hostimport.fileEmpty')
      return
    }
    const lines = countLines(content)
    if (lines > MAX_LINES) {
      fileError.value = t('hostimport.fileTooMany', { max: MAX_LINES })
      return
    }
    text.value = content
    fileName.value = file.name
    fileLines.value = lines
    error.value = null
    result.value = null
  } catch {
    fileError.value = t('hostimport.fileReadFailed')
  } finally {
    // 允许重复选择同一个文件时仍触发 change。
    input.value = ''
  }
}

function clearFile(): void {
  fileName.value = ''
  fileLines.value = 0
  fileError.value = null
}

function retryCredentials(): void {
  void loadCredentials()
}

async function submit(): Promise<void> {
  if (!canImport.value || submitting.value) return
  error.value = null
  notice.value = null
  if (validCount.value === 0) {
    error.value = t('import.errorEmpty')
    return
  }
  if (!credentialId.value) {
    error.value = t('import.errorCredential')
    return
  }
  submitting.value = true
  result.value = null
  try {
    const payload = {
      text: text.value,
      default_port: Number(defaultPort.value) || 22,
      credential_id: credentialId.value,
      group: group.value.trim() || undefined,
      tags: tags.value
        .split(',')
        .map((item) => item.trim())
        .filter(Boolean),
      mode: mode.value,
    }
    const response = await api.importServers(payload)
    result.value = response
    notice.value = t('import.done', { created: response.created.length, failed: response.failed.length })
  } catch (err) {
    error.value = errorMessage(err, t('common.error'))
  } finally {
    submitting.value = false
  }
}

onMounted(() => {
  if (!canImport.value) return
  void loadCredentials()
  void loadNetwork()
})
</script>

<template>
  <section class="ls-adminpage ls-importpage">
    <div v-if="!canImport" id="import-no-permission" class="ls-notice ls-notice--error" role="alert">
      <p class="ls-notice__title">{{ t('hostimport.noPermissionTitle') }}</p>
      <p class="ls-notice__text">{{ t('hostimport.noPermissionHint') }}</p>
    </div>

    <template v-else>
      <header class="ls-adminpage__head">
        <div class="ls-adminpage__heading">
          <h1 class="ls-adminpage__title">{{ t('import.title') }}</h1>
          <p class="ls-adminpage__subtitle">{{ t('import.description') }}</p>
        </div>
        <div class="ls-adminpage__head-actions">
          <button id="import-retry" class="ls-btn ls-btn--small" type="button" @click="retryCredentials">
            {{ t('common.refresh') }}
          </button>
        </div>
      </header>

      <div v-if="credentialState === 'error'" id="import-load-error" class="ls-notice ls-notice--error" role="alert">
        <p class="ls-notice__title">{{ t('hostimport.loadFailed') }}</p>
        <p class="ls-notice__text">{{ credentialError }}</p>
        <button class="ls-btn ls-btn--small" type="button" @click="retryCredentials">
          {{ t('common.retry') }}
        </button>
      </div>

      <p v-if="notice" id="import-notice" class="ls-notice ls-notice--success" role="status">
        <span class="ls-notice__text">{{ notice }}</span>
      </p>

      <div class="ls-importpage__grid">
        <section class="ls-importpage__card" aria-labelledby="import-form-title">
          <h2 id="import-form-title" class="ls-importpage__card-title">{{ t('import.pasteLabel') }}</h2>

          <div class="ls-importpage__file">
            <input
              id="import-file"
              class="ls-visually-hidden ls-importpage__file-input"
              type="file"
              accept=".txt,.csv,text/plain"
              @change="onFileChange"
            />
            <label for="import-file" class="ls-btn ls-importpage__file-label">
              {{ t('hostimport.fileButton') }}
            </label>
            <span class="ls-field__hint">{{ t('hostimport.fileHint', { max: MAX_LINES }) }}</span>
          </div>

          <p v-if="fileName" id="import-file-name" class="ls-importpage__file-name">
            <span>{{ t('hostimport.fileSelected', { name: fileName, lines: fileLines }) }}</span>
            <button id="import-file-clear" class="ls-btn ls-btn--small" type="button" @click="clearFile">
              {{ t('hostimport.fileClear') }}
            </button>
          </p>
          <p v-if="fileError" id="import-file-error" class="ls-adminpage__form-error" role="alert">
            {{ fileError }}
          </p>

          <label class="ls-field">
            <span class="ls-field__label">{{ t('import.pasteLabel') }}</span>
            <textarea
              id="import-text"
              v-model="text"
              class="ls-input ls-importpage__textarea"
              rows="10"
              :placeholder="t('import.pasteHint')"
            ></textarea>
            <span class="ls-field__hint">{{ t('import.pasteHint') }}</span>
          </label>

          <div class="ls-importpage__form-grid">
            <label class="ls-field">
              <span class="ls-field__label">{{ t('import.defaultPort') }}</span>
              <input id="import-port" v-model="defaultPort" class="ls-input" type="number" min="1" max="65535" />
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('import.mode') }}</span>
              <select id="import-mode" v-model="mode" class="ls-input">
                <option value="ssh">{{ t('hostimport.modeSsh') }}</option>
                <option value="agent">{{ t('hostimport.modeAgent') }}</option>
              </select>
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('import.credential') }}</span>
              <select id="import-credential" v-model="credentialId" class="ls-input">
                <option value="">{{ t('import.selectCredential') }}</option>
                <option v-for="credential in credentials" :key="credential.id" :value="credential.id">
                  {{ credential.name }}
                </option>
              </select>
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('import.group') }}</span>
              <input id="import-group" v-model="group" class="ls-input" type="text" autocomplete="off" />
            </label>
            <label class="ls-field">
              <span class="ls-field__label">{{ t('import.tags') }}</span>
              <input id="import-tags" v-model="tags" class="ls-input" type="text" autocomplete="off" />
            </label>
          </div>

          <p id="import-parsed" class="ls-importpage__parsed">
            {{ t('import.parsed', { valid: validCount, total: parsed.length }) }}
          </p>

          <p v-if="error" id="import-error" class="ls-adminpage__form-error" role="alert">{{ error }}</p>

          <button
            id="import-submit"
            class="ls-btn ls-btn--primary ls-btn--block"
            type="button"
            :disabled="submitting"
            :aria-busy="submitting"
            @click="submit"
          >
            <span v-if="submitting" class="ls-spinner" aria-hidden="true"></span>
            {{ submitting ? t('hostimport.submitting') : t('import.submit') }}
          </button>
        </section>

        <section class="ls-importpage__card" aria-labelledby="import-preview-title">
          <h2 id="import-preview-title" class="ls-importpage__card-title">{{ t('import.preview') }}</h2>

          <div v-if="parsed.length === 0" id="import-preview-empty" class="ls-empty">
            <h3 class="ls-empty__title">{{ t('import.previewEmptyTitle') }}</h3>
            <p class="ls-empty__text">{{ t('import.previewEmptyDescription') }}</p>
          </div>

          <div v-else class="ls-importpage__preview">
            <table class="ls-adminpage__table ls-importpage__preview-table">
              <caption class="ls-visually-hidden">{{ t('import.preview') }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t('import.host') }}</th>
                  <th scope="col">{{ t('import.port') }}</th>
                  <th scope="col">{{ t('import.status') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="row in parsed"
                  :key="row.key"
                  class="ls-importpage__preview-row"
                  :data-note="row.note"
                >
                  <td data-label="">{{ row.host }}</td>
                  <td :data-label="t('import.port')">{{ row.port }}</td>
                  <td :data-label="t('import.status')">
                    <span class="ls-chip" :class="previewStatusMeta(row.note).className">
                      {{ t(previewStatusMeta(row.note).labelKey) }}
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>

      <section v-if="result" id="import-result" class="ls-importpage__result" aria-labelledby="import-result-title">
        <h2 id="import-result-title" class="ls-importpage__card-title">{{ t('import.result') }}</h2>
        <p id="import-result-summary" class="ls-importpage__result-summary">
          {{
            t('import.resultSummary', {
              total: result.total,
              created: result.created.length,
              failed: result.failed.length,
            })
          }}
        </p>
        <ul class="ls-importpage__result-list">
          <li v-for="item in result.created" :key="`ok-${item.server_id}`" class="ls-importpage__created">
            <span class="ls-importpage__result-mark" aria-hidden="true">✓</span>
            <code class="ls-importpage__host">{{ item.host }}</code>
            <RouterLink class="ls-importpage__link" :to="`/servers/${item.server_id}`">
              {{ t('import.openServer') }}
            </RouterLink>
          </li>
          <li v-for="item in result.failed" :key="`fail-${item.host}`" class="ls-importpage__failed">
            <span class="ls-importpage__result-mark" aria-hidden="true">✗</span>
            <code class="ls-importpage__host">{{ item.host }}</code>
            <span class="ls-importpage__error-text">{{ item.error }}</span>
          </li>
        </ul>
      </section>
    </template>
  </section>
</template>
