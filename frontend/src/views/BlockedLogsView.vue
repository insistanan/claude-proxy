<template>
  <div class="blocked-logs-view">
    <header class="page-toolbar">
      <div class="page-title">
        <v-icon size="22" color="error">mdi-shield-alert</v-icon>
        <h1 class="text-h5 font-weight-bold">拦截记录</h1>
        <v-chip size="small" variant="tonal">{{ total }}</v-chip>
      </div>
      <div class="toolbar-actions">
        <v-btn variant="tonal" prepend-icon="mdi-refresh" :loading="loading" @click="loadLogs">刷新</v-btn>
        <v-btn
          color="error"
          variant="tonal"
          prepend-icon="mdi-delete"
          :disabled="total === 0"
          @click="clearDialog = true"
        >
          清空全部
        </v-btn>
      </div>
    </header>

    <section class="filters" aria-label="拦截记录筛选">
      <v-select
        v-model="apiType"
        :items="apiTypeItems"
        label="API 类型"
        density="compact"
        hide-details
      />
      <v-select
        v-model="blockType"
        :items="blockTypeItems"
        label="拦截类型"
        density="compact"
        hide-details
      />
      <v-text-field
        v-model="fromTime"
        type="datetime-local"
        label="开始时间"
        density="compact"
        hide-details
      />
      <v-text-field
        v-model="toTime"
        type="datetime-local"
        label="结束时间"
        density="compact"
        hide-details
      />
      <v-btn color="primary" prepend-icon="mdi-magnify" class="filter-action" @click="applyFilters">应用</v-btn>
      <v-btn variant="text" class="filter-action" :disabled="!hasFilters" @click="resetFilters">重置</v-btn>
    </section>

    <v-alert v-if="error" type="error" variant="tonal" density="compact" closable @click:close="error = ''">
      {{ error }}
    </v-alert>

    <div class="table-frame">
      <v-table density="compact" class="blocked-logs-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>入口</th>
            <th>类型 / 规则</th>
            <th>模型 / 渠道</th>
            <th>请求 ID</th>
            <th>命中片段</th>
            <th class="action-column">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="7" class="text-center text-medium-emphasis py-10">
              <v-progress-circular indeterminate size="24" width="2" color="primary" />
            </td>
          </tr>
          <tr v-else-if="logs.length === 0">
            <td colspan="7" class="empty-cell">
              <v-icon size="28" color="secondary">mdi-shield-alert</v-icon>
              <span>暂无拦截记录</span>
            </td>
          </tr>
          <template v-else>
            <tr v-for="entry in logs" :key="entry.id">
              <td class="time-cell">{{ formatTime(entry.timestamp) }}</td>
              <td>
                <v-chip size="x-small" :color="apiTypeColor(entry.apiType)" variant="tonal">
                  {{ apiTypeLabel(entry.apiType) }}
                </v-chip>
              </td>
              <td class="type-cell">
                <v-chip size="x-small" :color="blockTypeColor(entry.blockType)" variant="tonal">
                  {{ blockTypeLabel(entry.blockType) }}
                </v-chip>
                <div class="text-caption text-medium-emphasis mt-1">{{ ruleLabel(entry.ruleName) }}</div>
              </td>
              <td class="metadata-cell">
                <div class="font-weight-medium">{{ entry.model || '--' }}</div>
                <div class="text-caption text-medium-emphasis">{{ entry.channelName || '--' }}</div>
              </td>
              <td class="request-id-cell" :title="entry.requestId || ''">{{ entry.requestId || '--' }}</td>
              <td class="snippet-cell" :title="entry.promptSnippet || ''">{{ entry.promptSnippet || '--' }}</td>
              <td class="action-cell">
                <v-tooltip text="查看详情">
                  <template #activator="{ props }">
                    <v-btn
                      v-bind="props"
                      icon="mdi-open-in-new"
                      variant="text"
                      size="small"
                      class="table-action"
                      :aria-label="`查看记录 ${entry.id}`"
                      @click="openDetail(entry.id)"
                    />
                  </template>
                </v-tooltip>
                <v-tooltip text="删除记录">
                  <template #activator="{ props }">
                    <v-btn
                      v-bind="props"
                      icon="mdi-delete"
                      color="error"
                      variant="text"
                      size="small"
                      class="table-action"
                      :aria-label="`删除记录 ${entry.id}`"
                      @click="openDelete(entry)"
                    />
                  </template>
                </v-tooltip>
              </td>
            </tr>
          </template>
        </tbody>
      </v-table>
    </div>

    <footer class="pagination-bar">
      <div class="text-body-2 text-medium-emphasis">第 {{ page }} 页，共 {{ pageCount }} 页</div>
      <v-pagination
        v-model="page"
        :length="pageCount"
        :total-visible="5"
        density="comfortable"
        aria-label="拦截记录分页"
      />
      <v-select
        v-model="pageSize"
        :items="pageSizeItems"
        label="每页"
        density="compact"
        hide-details
        class="page-size-select"
      />
    </footer>

    <v-dialog v-model="detailDialog" max-width="680">
      <v-card>
        <v-card-title class="dialog-title">
          <span>拦截详情</span>
          <v-chip v-if="selectedLog" size="small" :color="blockTypeColor(selectedLog.blockType)" variant="tonal">
            {{ blockTypeLabel(selectedLog.blockType) }}
          </v-chip>
        </v-card-title>
        <v-divider />
        <v-card-text>
          <v-progress-linear v-if="detailLoading" indeterminate color="primary" />
          <dl v-else-if="selectedLog" class="detail-grid">
            <dt>时间</dt><dd>{{ formatTime(selectedLog.timestamp) }}</dd>
            <dt>API 类型</dt><dd>{{ apiTypeLabel(selectedLog.apiType) }}</dd>
            <dt>规则</dt><dd>{{ ruleLabel(selectedLog.ruleName) }}</dd>
            <dt>模型</dt><dd>{{ selectedLog.model || '--' }}</dd>
            <dt>渠道</dt><dd>{{ selectedLog.channelName || '--' }}</dd>
            <dt>请求 ID</dt><dd class="break-text">{{ selectedLog.requestId || '--' }}</dd>
            <dt>命中片段</dt>
            <dd><pre class="snippet-detail">{{ selectedLog.promptSnippet || '--' }}</pre></dd>
          </dl>
        </v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="detailDialog = false">关闭</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="deleteDialog" max-width="440">
      <v-card>
        <v-card-title>删除拦截记录</v-card-title>
        <v-card-text>确定删除这条拦截记录吗？</v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="deleteDialog = false">取消</v-btn>
          <v-btn color="error" :loading="deleting" @click="confirmDelete">删除</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="clearDialog" max-width="440">
      <v-card>
        <v-card-title>清空拦截记录</v-card-title>
        <v-card-text>确定清空全部 {{ total }} 条拦截记录吗？此操作无法撤销。</v-card-text>
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="clearDialog = false">取消</v-btn>
          <v-btn color="error" :loading="deleting" @click="confirmClear">清空全部</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  api,
  type BlockedLogEntry,
  type BlockedLogType,
  type ContentSafetyAPIType
} from '@/services/api'

const apiTypeItems: Array<{ title: string; value: ContentSafetyAPIType | '' }> = [
  { title: '全部入口', value: '' },
  { title: 'Messages', value: 'messages' },
  { title: 'Responses', value: 'responses' },
  { title: 'Chat', value: 'chat' },
  { title: 'Gemini', value: 'gemini' }
]

const blockTypeItems: Array<{ title: string; value: BlockedLogType | '' }> = [
  { title: '全部类型', value: '' },
  { title: '敏感词', value: 'sensitive_word' },
  { title: '个人信息', value: 'sensitive_info' },
  { title: '凭据', value: 'credential' },
  { title: '危险命令', value: 'dangerous_cmd' }
]

const pageSizeItems = [20, 50, 100]
const apiType = ref<ContentSafetyAPIType | ''>('')
const blockType = ref<BlockedLogType | ''>('')
const fromTime = ref('')
const toTime = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const logs = ref<BlockedLogEntry[]>([])
const loading = ref(false)
const deleting = ref(false)
const error = ref('')
const detailDialog = ref(false)
const detailLoading = ref(false)
const selectedLog = ref<BlockedLogEntry | null>(null)
const deleteDialog = ref(false)
const deletingLog = ref<BlockedLogEntry | null>(null)
const clearDialog = ref(false)

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const hasFilters = computed(() => Boolean(apiType.value || blockType.value || fromTime.value || toTime.value))

const asRFC3339 = (value: string) => value ? new Date(value).toISOString() : ''

const loadLogs = async () => {
  loading.value = true
  error.value = ''
  try {
    const response = await api.getBlockedLogs({
      apiType: apiType.value,
      blockType: blockType.value,
      from: asRFC3339(fromTime.value),
      to: asRFC3339(toTime.value),
      page: page.value,
      pageSize: pageSize.value
    })
    logs.value = response.logs || []
    total.value = response.total || 0
    if (page.value > pageCount.value) {
      page.value = pageCount.value
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : '加载拦截记录失败'
    logs.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

const applyFilters = () => {
  if (fromTime.value && toTime.value && new Date(fromTime.value) > new Date(toTime.value)) {
    error.value = '开始时间不能晚于结束时间'
    return
  }
  if (page.value === 1) loadLogs()
  else page.value = 1
}

const resetFilters = () => {
  apiType.value = ''
  blockType.value = ''
  fromTime.value = ''
  toTime.value = ''
  applyFilters()
}

const openDetail = async (id: number) => {
  detailDialog.value = true
  detailLoading.value = true
  selectedLog.value = null
  try {
    selectedLog.value = await api.getBlockedLog(id)
  } catch (err) {
    detailDialog.value = false
    error.value = err instanceof Error ? err.message : '加载拦截详情失败'
  } finally {
    detailLoading.value = false
  }
}

const openDelete = (entry: BlockedLogEntry) => {
  deletingLog.value = entry
  deleteDialog.value = true
}

const confirmDelete = async () => {
  if (!deletingLog.value) return
  deleting.value = true
  error.value = ''
  try {
    await api.deleteBlockedLog(deletingLog.value.id)
    deleteDialog.value = false
    deletingLog.value = null
    await loadLogs()
  } catch (err) {
    error.value = err instanceof Error ? err.message : '删除拦截记录失败'
  } finally {
    deleting.value = false
  }
}

const confirmClear = async () => {
  deleting.value = true
  error.value = ''
  try {
    await api.clearBlockedLogs()
    clearDialog.value = false
    if (page.value === 1) await loadLogs()
    else page.value = 1
  } catch (err) {
    error.value = err instanceof Error ? err.message : '清空拦截记录失败'
  } finally {
    deleting.value = false
  }
}

const apiTypeLabel = (value: ContentSafetyAPIType) => ({
  messages: 'Messages', responses: 'Responses', chat: 'Chat', gemini: 'Gemini'
})[value]

const apiTypeColor = (value: ContentSafetyAPIType) => ({
  messages: 'primary', responses: 'secondary', chat: 'info', gemini: 'success'
})[value]

const blockTypeLabel = (value: BlockedLogType) => ({
  sensitive_word: '敏感词', sensitive_info: '个人信息', credential: '凭据', dangerous_cmd: '危险命令'
})[value]

const blockTypeColor = (value: BlockedLogType) => ({
  sensitive_word: 'error', sensitive_info: 'warning', credential: 'error', dangerous_cmd: 'error'
})[value]

const ruleLabels: Record<string, string> = {
  pornography: '色情',
  gambling: '赌博',
  drugs: '毒品',
  violence_terror: '暴力与恐怖',
  political: '政治敏感',
  illegal_crime: '违法犯罪',
  custom: '自定义敏感词',
  api_key: 'API Key',
  named_secret: '密码与命名密钥',
  private_key: 'PEM 私钥',
  connection_string: '数据库连接串',
  high_entropy: '高熵环境变量值',
  phone: '手机号',
  id_card: '身份证号',
  email: '邮箱',
  ip_address: 'IP 地址',
  destructive: '破坏性命令',
  download_execute: '远程下载执行',
  reverse_shell: '反弹 Shell',
  privilege_escalation: '权限提升',
  environment_tampering: '环境变量篡改'
}

const ruleLabel = (value?: string) => value ? ruleLabels[value] || value : '--'

const formatTime = (value?: string) => {
  if (!value) return '--'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

watch(page, loadLogs)
watch(pageSize, () => {
  if (page.value === 1) loadLogs()
  else page.value = 1
})

onMounted(loadLogs)
</script>

<style scoped>
.blocked-logs-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
  max-width: 1480px;
  margin: 0 auto;
}

.page-toolbar,
.page-title,
.toolbar-actions,
.pagination-bar,
.dialog-title,
.action-cell {
  display: flex;
  align-items: center;
}

.page-toolbar {
  justify-content: space-between;
  gap: 16px;
  padding: 16px 0 4px;
}

.page-title,
.toolbar-actions,
.dialog-title,
.action-cell {
  gap: 10px;
}

.filters {
  display: grid;
  grid-template-columns: minmax(140px, 0.8fr) minmax(150px, 0.9fr) minmax(190px, 1fr) minmax(190px, 1fr) auto auto;
  gap: 10px;
  align-items: center;
  padding: 14px;
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 6px;
  background: rgb(var(--v-theme-surface));
}

.filter-action,
.table-action {
  min-height: 44px;
}

.table-action {
  min-width: 44px;
}

.table-frame {
  overflow-x: auto;
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 6px;
  background: rgb(var(--v-theme-surface));
}

.blocked-logs-table {
  min-width: 1060px;
}

.blocked-logs-table :deep(th) {
  color: rgb(var(--v-theme-on-surface));
  background: rgba(var(--v-theme-primary), 0.07);
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;
}

.blocked-logs-table :deep(td) {
  height: 58px;
  font-size: 13px;
  vertical-align: middle;
}

.time-cell {
  width: 168px;
  white-space: nowrap;
}

.type-cell,
.metadata-cell {
  min-width: 150px;
}

.request-id-cell {
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.snippet-cell {
  max-width: 340px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.action-column,
.action-cell {
  width: 108px;
}

.empty-cell {
  height: 140px !important;
  text-align: center;
  color: rgb(var(--v-theme-on-surface-variant));
}

.empty-cell > * + * {
  margin-left: 10px;
}

.pagination-bar {
  display: grid;
  grid-template-columns: minmax(130px, 1fr) auto minmax(100px, 1fr);
  gap: 16px;
  min-height: 52px;
}

.page-size-select {
  width: 104px;
  justify-self: end;
}

.dialog-title {
  justify-content: space-between;
}

.detail-grid {
  display: grid;
  grid-template-columns: 92px minmax(0, 1fr);
  gap: 12px 18px;
  margin: 0;
}

.detail-grid dt {
  color: rgb(var(--v-theme-on-surface-variant));
  font-size: 13px;
}

.detail-grid dd {
  min-width: 0;
  margin: 0;
}

.break-text,
.snippet-detail {
  overflow-wrap: anywhere;
}

.snippet-detail {
  max-height: 240px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 4px;
  background: rgba(var(--v-theme-surface-variant), 0.35);
  font: inherit;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  white-space: pre-wrap;
}

@media (max-width: 1100px) {
  .filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 700px) {
  .page-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }

  .toolbar-actions {
    width: 100%;
    flex-wrap: wrap;
  }

  .filters {
    grid-template-columns: 1fr;
  }

  .pagination-bar {
    grid-template-columns: 1fr;
    justify-items: center;
  }

  .page-size-select {
    justify-self: center;
  }

  .detail-grid {
    grid-template-columns: 1fr;
    gap: 4px;
  }

  .detail-grid dd + dt {
    margin-top: 10px;
  }
}
</style>
