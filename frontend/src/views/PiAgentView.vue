<template>
  <div class="agent-config-page piagent-page">
    <AgentConfigHeader title="pi-agent 配置" subtitle="管理当前服务运行用户的 pi-agent 模型供应商、凭据与备份">
      <v-btn variant="text" prepend-icon="mdi-refresh" :loading="loading" @click="loadAll">刷新</v-btn>
    </AgentConfigHeader>

    <v-alert v-if="disabled" type="warning" variant="tonal" class="mb-4">
      pi-agent 配置目录不可用。请确认 <code>~/.pi/agent</code> 目录存在并可写。
    </v-alert>

    <v-alert v-if="loadError" type="error" variant="tonal" class="mb-4" closable @click:close="loadError = ''">
      {{ loadError }}
    </v-alert>

    <v-alert v-if="notice.visible" :type="notice.type" variant="tonal" density="compact" class="mb-4" closable @click:close="notice.visible = false">
      {{ notice.message }}
    </v-alert>

    <v-progress-linear v-if="loading" indeterminate color="primary" class="mb-4" />

    <template v-if="!disabled && !loading">
      <AgentConfigLocation v-if="status" label="配置目录" :path="status.configDir">
        <v-chip v-if="!status.exists" size="x-small" color="warning" label>目录不存在</v-chip>
        <v-chip v-else-if="!status.writable" size="x-small" color="error" label>不可写</v-chip>
        <v-chip v-else size="x-small" color="success" label>可写</v-chip>
      </AgentConfigLocation>

      <v-tabs v-model="activeTab" color="primary" class="agent-config-tabs">
        <v-tab value="providers" prepend-icon="mdi-server-network">供应商</v-tab>
        <v-tab value="models" prepend-icon="mdi-tune">默认模型</v-tab>
        <v-tab value="credentials" prepend-icon="mdi-key-chain">凭据</v-tab>
        <v-tab value="backups" prepend-icon="mdi-backup-restore">备份</v-tab>
      </v-tabs>

      <v-window v-model="activeTab">
        <!-- 供应商 -->
        <v-window-item value="providers">
          <v-row>
            <v-col cols="12" lg="4">
              <v-card class="provider-list-card" elevation="0">
                <div class="agent-config-panel-header">
                  <div class="agent-config-panel-title">供应商</div>
                    <v-btn icon="mdi-plus" size="small" variant="text" title="添加供应商" aria-label="添加供应商" @click="addProvider" />
                </div>
                <v-divider />
                <v-list v-if="providers.length" density="comfortable" nav class="py-2">
                  <v-list-item v-for="provider in providers" :key="provider.localId" :active="selectedProviderId === provider.localId" :title="provider.name || provider.id" :subtitle="provider.id" rounded="sm" @click="selectProvider(provider.localId)">
                    <template #prepend><v-icon size="20">mdi-server-network</v-icon></template>
                    <template #append>
                      <v-chip v-if="provider.apiKeyPresent" size="x-small" color="success" label>密钥</v-chip>
                      <v-chip v-else-if="provider.hasOAuth" size="x-small" color="info" label>OAuth</v-chip>
                    </template>
                  </v-list-item>
                </v-list>
                <div v-else class="empty-providers text-center text-medium-emphasis px-6 py-10">
                  <v-icon size="32" class="mb-3">mdi-server-network</v-icon>
                  <div class="text-body-2">尚未配置供应商</div>
                  <v-btn class="mt-4" size="small" color="primary" variant="tonal" prepend-icon="mdi-plus" @click="addProvider">添加供应商</v-btn>
                </div>
              </v-card>
            </v-col>

            <v-col cols="12" lg="8">
              <v-card v-if="selectedProvider" elevation="0" class="provider-editor-card">
                <div class="agent-config-panel-header">
                  <div>
                    <div class="agent-config-panel-title">{{ selectedProvider.name || '新供应商' }}</div>
                    <div class="agent-config-panel-subtitle">写入 pi-agent 的 models.json providers 字段</div>
                  </div>
                  <div class="agent-config-panel-actions">
                    <v-tooltip text="连通性测试"><template #activator="{ props }"><v-btn v-bind="props" icon="mdi-test-tube" size="small" variant="text" aria-label="测试供应商连通性" :disabled="!isExistingProvider || discovering" :loading="testing" @click="testProvider" /></template></v-tooltip>
                    <v-tooltip text="发现 OpenAI 兼容模型"><template #activator="{ props }"><v-btn v-bind="props" icon="mdi-magnify" size="small" variant="text" aria-label="发现 OpenAI 兼容模型" :disabled="!isExistingProvider || testing || (Boolean(selectedProvider.api) && !['openai-completions', 'openai-responses'].includes(selectedProvider.api))" :loading="discovering" @click="discoverModels" /></template></v-tooltip>
                    <v-btn color="error" variant="text" size="small" prepend-icon="mdi-delete" :disabled="!isExistingProvider" @click="removeProvider(selectedProvider.id)">删除</v-btn>
                    <v-btn color="primary" size="small" prepend-icon="mdi-content-save" :loading="savingProvider" @click="saveSelectedProvider">保存</v-btn>
                  </div>
                </div>
                <v-divider />
                <v-card-text class="pa-5">
                  <v-row>
                    <v-col cols="12" sm="6">
                      <v-text-field v-model="selectedProvider.id" label="供应商标识" variant="outlined" density="comfortable" :disabled="isExistingProvider" hint="例如 anthropic；模型引用将使用 provider/model" persistent-hint />
                    </v-col>
                    <v-col cols="12" sm="6">
                      <v-text-field v-model="selectedProvider.name" label="显示名称" variant="outlined" density="comfortable" placeholder="例如 Anthropic" />
                    </v-col>
                    <v-col cols="12" sm="6">
                      <v-select v-model="selectedProvider.api" label="API 协议" variant="outlined" density="comfortable" :items="apiProtocols" item-title="title" item-value="value" clearable placeholder="不设置时使用默认协议" />
                    </v-col>
                    <v-col cols="12" sm="6">
                      <v-text-field v-model="selectedProvider.baseUrl" label="Base URL" variant="outlined" density="comfortable" placeholder="例如 https://api.anthropic.com" />
                    </v-col>
                    <v-col cols="12" sm="6">
                      <v-text-field v-model="selectedProvider.apiKey" label="API Key" type="password" variant="outlined" density="comfortable" :placeholder="selectedProvider.apiKeyPresent ? '当前：' + selectedProvider.apiKeyMasked + '（留空则保留）' : '填写 API Key'" />
                    </v-col>
                  </v-row>
                  <v-expansion-panels variant="accordion" class="mt-2">
                    <v-expansion-panel title="高级选项">
                      <v-expansion-panel-text>
                        <v-row>
                          <v-col cols="12" md="4">
                            <v-textarea v-model="selectedProvider.headersText" label="请求头 JSON" variant="outlined" density="comfortable" rows="4" auto-grow />
                          </v-col>
                          <v-col cols="12" md="4">
                            <v-textarea v-model="selectedProvider.compatText" label="compat 兼容配置 JSON" variant="outlined" density="comfortable" rows="4" auto-grow />
                          </v-col>
                          <v-col cols="12" md="4">
                            <v-textarea v-model="selectedProvider.modelOverridesText" label="模型覆盖 JSON" variant="outlined" density="comfortable" rows="4" auto-grow />
                          </v-col>
                        </v-row>
                      </v-expansion-panel-text>
                    </v-expansion-panel>
                  </v-expansion-panels>

                  <div class="d-flex align-center justify-space-between mt-6 mb-3">
                    <div>
                      <div class="text-subtitle-1 font-weight-bold">模型</div>
                      <div class="text-caption text-medium-emphasis">模型 ID 必须与上游 API 接收的模型名一致</div>
                    </div>
                    <v-btn size="small" color="primary" variant="tonal" prepend-icon="mdi-plus" @click="addModel">添加模型</v-btn>
                  </div>

                  <div v-if="selectedProvider.models.length === 0" class="empty-models text-body-2 text-medium-emphasis py-6 text-center">添加至少一个模型后，它才会出现在 pi-agent 的模型列表中。</div>
                  <v-expansion-panels v-else variant="accordion" class="model-panels">
                    <v-expansion-panel v-for="(model, index) in selectedProvider.models" :key="model.localId">
                      <template #title>
                        <div class="d-flex align-center ga-2 overflow-hidden">
                          <span class="font-weight-medium text-truncate">{{ model.name || model.id || '新模型' }}</span>
                          <span v-if="model.id" class="text-caption text-medium-emphasis text-truncate">{{ model.id }}</span>
                        </div>
                      </template>
                      <v-expansion-panel-text>
                        <v-row>
                          <v-col cols="12" sm="6">
                            <v-text-field v-model="model.id" label="模型 ID" variant="outlined" density="comfortable" placeholder="例如 claude-opus-4-8" />
                          </v-col>
                          <v-col cols="12" sm="6">
                            <v-text-field v-model="model.name" label="显示名称" variant="outlined" density="comfortable" placeholder="例如 Claude Opus 4.8" />
                          </v-col>
                          <v-col cols="12" sm="6">
                            <v-select v-model="model.api" label="API 协议" variant="outlined" density="comfortable" :items="apiProtocols" item-title="title" item-value="value" clearable />
                          </v-col>
                          <v-col cols="12" sm="6">
                            <v-text-field v-model="model.baseUrl" label="Base URL" variant="outlined" density="comfortable" placeholder="继承 provider 的 Base URL" />
                          </v-col>
                          <v-col cols="12" sm="4">
                            <v-select v-model="model.reasoning" label="思考能力" variant="outlined" density="comfortable" :items="reasoningOptions" item-title="title" item-value="value" />
                          </v-col>
                          <v-col cols="12" sm="4">
                            <v-text-field v-model.number="model.contextWindow" label="上下文窗口" type="number" min="0" variant="outlined" density="comfortable" />
                          </v-col>
                          <v-col cols="12" sm="4">
                            <v-text-field v-model.number="model.maxTokens" label="最大输出 Token" type="number" min="0" variant="outlined" density="comfortable" />
                          </v-col>
                          <v-col cols="12" sm="6">
                            <v-text-field v-model="model.inputText" label="输入能力（逗号分隔）" variant="outlined" density="comfortable" placeholder="text, image" />
                          </v-col>
                          <v-col cols="12" sm="6">
                            <v-switch v-model="model.inputImage" label="支持图片" color="primary" hide-details />
                          </v-col>
                        </v-row>

                        <!-- 费用 -->
                        <v-expansion-panels variant="accordion" class="mt-2">
                          <v-expansion-panel title="费用（每百万 token 美元）">
                            <v-expansion-panel-text>
                              <v-row>
                                <v-col cols="6" sm="3">
                                  <v-text-field v-model.number="model.costInput" label="输入" type="number" min="0" step="0.01" variant="outlined" density="comfortable" />
                                </v-col>
                                <v-col cols="6" sm="3">
                                  <v-text-field v-model.number="model.costOutput" label="输出" type="number" min="0" step="0.01" variant="outlined" density="comfortable" />
                                </v-col>
                                <v-col cols="6" sm="3">
                                  <v-text-field v-model.number="model.costCacheRead" label="缓存读取" type="number" min="0" step="0.01" variant="outlined" density="comfortable" />
                                </v-col>
                                <v-col cols="6" sm="3">
                                  <v-text-field v-model.number="model.costCacheWrite" label="缓存写入" type="number" min="0" step="0.01" variant="outlined" density="comfortable" />
                                </v-col>
                              </v-row>
                            </v-expansion-panel-text>
                          </v-expansion-panel>
                        </v-expansion-panels>

                        <!-- 思考等级 -->
                        <div class="mt-3 mb-1">
                          <div class="text-body-2 font-weight-medium">思考等级映射（token 预算）</div>
                          <div class="text-caption text-medium-emphasis">关闭/不支持的等级留空，开启的填写 token 预算数</div>
                        </div>
                        <v-row>
                          <v-col v-for="tl in thinkingLevelsList" :key="tl.key" cols="6" sm="4" md="3">
                            <div class="d-flex align-center ga-1">
                              <v-checkbox v-model="model.tlEnabled[tl.key]" :label="tl.label" density="compact" hide-details class="tl-checkbox" />
                              <v-text-field v-model="model.tlBudget[tl.key]" :disabled="!model.tlEnabled[tl.key]" variant="outlined" density="compact" type="number" min="0" placeholder="token" hide-details style="max-width: 100px;" />
                            </div>
                          </v-col>
                        </v-row>

                        <!-- 高级 JSON -->
                        <v-expansion-panels variant="accordion" class="mt-2">
                          <v-expansion-panel title="高级 JSON 字段">
                            <v-expansion-panel-text>
                              <v-row>
                                <v-col cols="12" md="6">
                                  <v-textarea v-model="model.headersText" label="请求头 JSON" variant="outlined" density="comfortable" rows="3" auto-grow />
                                </v-col>
                                <v-col cols="12" md="6">
                                  <v-textarea v-model="model.compatText" label="compat JSON" variant="outlined" density="comfortable" rows="3" auto-grow />
                                </v-col>
                              </v-row>
                            </v-expansion-panel-text>
                          </v-expansion-panel>
                        </v-expansion-panels>

                        <div class="d-flex justify-end mt-2">
                          <v-btn size="small" color="error" variant="text" prepend-icon="mdi-delete" @click="removeModel(index)">删除模型</v-btn>
                        </div>
                      </v-expansion-panel-text>
                    </v-expansion-panel>
                  </v-expansion-panels>
                </v-card-text>
              </v-card>

              <v-card v-else elevation="0" class="empty-editor">
                <div class="empty-providers text-center text-medium-emphasis px-6 py-16">
                  <v-icon size="40" class="mb-3">mdi-server-network</v-icon>
                  <div class="text-body-1 mb-2">选择一个供应商或创建新的供应商</div>
                  <div class="text-caption">支持 anthropic-messages / openai-completions / openai-responses / google-generative-ai 四种协议</div>
                </div>
              </v-card>
            </v-col>
          </v-row>
        </v-window-item>

        <!-- 默认模型 -->
        <v-window-item value="models">
          <v-card elevation="0" class="agent-config-panel">
            <div class="agent-config-panel-header">
              <div>
                <div class="agent-config-panel-title">默认模型设置</div>
                <div class="agent-config-panel-subtitle">写入 pi-agent 的 settings.json，其余字段保持不变</div>
              </div>
              <v-btn color="primary" prepend-icon="mdi-content-save" :loading="savingSettings" @click="saveModelSettings">保存设置</v-btn>
            </div>
            <v-divider />
            <v-card-text class="pa-5">
              <v-row>
                <v-col cols="12" md="6">
                  <v-select v-model="modelSettings.defaultProvider" label="默认供应商" variant="outlined" density="comfortable" :items="providers.map(p => ({ title: p.name || p.id, value: p.id }))" item-title="title" item-value="value" clearable />
                </v-col>
                <v-col cols="12" md="6">
                  <v-text-field v-model="modelSettings.defaultModel" label="默认模型" variant="outlined" density="comfortable" placeholder="例如 anthropic/claude-opus-4-8" />
                </v-col>
                <v-col cols="12" md="6">
                  <v-select v-model="modelSettings.defaultThinkingLevel" label="默认思考等级" variant="outlined" density="comfortable" :items="thinkingLevelsSettings" item-title="title" item-value="value" clearable />
                </v-col>
                <v-col cols="12" md="6">
                  <v-combobox v-model="modelSettings.enabledModels" label="启用的模型模式（回车添加）" variant="outlined" density="comfortable" multiple small-chips deletable-chips hint="支持 glob 模式，例如 anthropic/*" persistent-hint />
                </v-col>
              </v-row>
            </v-card-text>
          </v-card>
        </v-window-item>

        <!-- 凭据 -->
        <v-window-item value="credentials">
          <v-card elevation="0" class="agent-config-panel">
            <div class="agent-config-panel-header">
              <span class="agent-config-panel-title">凭据</span>
              <v-btn variant="text" icon="mdi-refresh" size="small" :loading="loading" aria-label="刷新凭据" title="刷新凭据" @click="loadCredentials" />
            </div>
            <v-divider />
            <v-table>
              <thead>
                <tr>
                  <th>供应商</th>
                  <th>类型</th>
                  <th>密钥状态</th>
                  <th>环境变量</th>
                  <th class="text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="credential in credentials" :key="credential.id">
                  <td class="font-weight-medium">{{ credential.id }}</td>
                  <td><v-chip size="x-small" :color="credentialTypeColor(credential.type)" label>{{ credentialTypeLabel(credential.type) }}</v-chip></td>
                  <td>
                    <template v-if="credential.type === 'oauth'"><span class="text-medium-emphasis">OAuth 会话中</span></template>
                    <template v-else-if="credential.keyPresent"><span class="mono-text">{{ credential.keyMasked }}</span></template>
                    <template v-else><span class="text-medium-emphasis">未配置</span></template>
                  </td>
                  <td>
                    <div v-if="credential.hasEnvValues" class="d-flex flex-wrap ga-1">
                      <v-chip v-for="(masked, ek) in credential.envMasked" :key="ek" size="x-small" variant="outlined" label>{{ ek }}={{ masked }}</v-chip>
                    </div>
                    <span v-else class="text-medium-emphasis">-</span>
                  </td>
                  <td class="text-right">
                    <v-btn v-if="credential.type === 'oauth'" size="small" color="error" variant="text" prepend-icon="mdi-logout" @click="removeCredential(credential)">退出</v-btn>
                    <v-btn v-else size="small" color="primary" variant="tonal" prepend-icon="mdi-key" @click="openCredentialDialog(credential)">更新密钥</v-btn>
                  </td>
                </tr>
                <tr v-if="credentials.length === 0"><td colspan="5" class="text-center text-medium-emphasis py-8">暂无凭据</td></tr>
              </tbody>
            </v-table>
          </v-card>
        </v-window-item>

        <!-- 备份 -->
        <v-window-item value="backups">
          <v-card elevation="0" class="agent-config-panel">
            <div class="agent-config-panel-header">
              <div>
                <div class="agent-config-panel-title">备份</div>
                <div class="agent-config-panel-subtitle">写入前自动备份，也可手动创建快照</div>
              </div>
              <div class="agent-config-panel-actions">
                <v-btn v-for="kind in backupKinds" :key="kind.value" size="small" variant="tonal" color="primary" prepend-icon="mdi-plus" :disabled="creatingBackup !== ''" :loading="creatingBackup === kind.value" @click="createBackup(kind.value)">备份 {{ kind.label }}</v-btn>
              </div>
            </div>
            <v-divider />
            <v-table>
              <thead>
                <tr>
                  <th>文件</th>
                  <th>创建时间</th>
                  <th>大小</th>
                  <th>Revision</th>
                  <th class="text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="backup in backups" :key="backup.id">
                  <td class="font-weight-medium">{{ backup.file }}<v-chip v-if="backup.sensitive" size="x-small" color="warning" label class="ml-2">含密钥</v-chip></td>
                  <td>{{ formatTime(backup.created) }}</td>
                  <td>{{ formatSize(backup.size) }}</td>
                  <td class="mono-text text-caption">{{ shortRevision(backup.revision) }}</td>
                  <td class="text-right"><v-btn size="small" color="warning" variant="text" prepend-icon="mdi-backup-restore" @click="restoreBackup(backup)">恢复</v-btn></td>
                </tr>
                <tr v-if="backups.length === 0"><td colspan="5" class="text-center text-medium-emphasis py-8">暂无备份</td></tr>
              </tbody>
            </v-table>
          </v-card>
        </v-window-item>
      </v-window>
    </template>

    <!-- 更新密钥对话框 -->
    <v-dialog v-model="credentialDialog.visible" max-width="480" persistent>
      <v-card>
        <v-card-title class="px-5 pt-4">更新密钥 - {{ credentialDialog.credential?.id }}</v-card-title>
        <v-card-text class="pa-5">
          <v-alert v-if="credentialDialog.credential?.keyPresent" type="info" variant="tonal" density="compact" class="mb-4">
            当前密钥：{{ credentialDialog.credential?.keyMasked }}。输入新密钥将替换，清空后选择移除。
          </v-alert>
          <v-text-field v-model="credentialDialog.key" label="新的 API Key" type="password" variant="outlined" density="comfortable" placeholder="可填写 {env:VARIABLE} 环境引用" />
          <div class="d-flex justify-space-between align-center mt-4">
            <v-btn v-if="credentialDialog.credential?.keyPresent" color="error" variant="text" prepend-icon="mdi-delete" :disabled="savingCredential" @click="submitCredential('remove')">移除密钥</v-btn>
            <v-spacer />
            <v-btn variant="text" :disabled="savingCredential" @click="credentialDialog.visible = false">取消</v-btn>
            <v-btn color="primary" prepend-icon="mdi-content-save" :loading="savingCredential" @click="submitCredential('replace')">保存</v-btn>
          </div>
        </v-card-text>
      </v-card>
    </v-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AgentConfigHeader from '@/components/AgentConfigHeader.vue'
import AgentConfigLocation from '@/components/AgentConfigLocation.vue'
import { api, type ApiError, type PiAgentBackup, type PiAgentCredential, type PiAgentFileKind, type PiAgentProvider, type PiAgentStatus } from '@/services/api'

const loading = ref(false)
const disabled = ref(false)
const loadError = ref('')
const notice = ref({ visible: false, type: 'success' as 'success' | 'error', message: '' })
const activeTab = ref('providers')
const status = ref<PiAgentStatus | null>(null)

const apiProtocols = [
  { title: 'Anthropic Messages', value: 'anthropic-messages' },
  { title: 'OpenAI Completions', value: 'openai-completions' },
  { title: 'OpenAI Responses', value: 'openai-responses' },
  { title: 'Google Generative AI', value: 'google-generative-ai' }
]
const reasoningOptions = [
  { title: '未设置', value: null },
  { title: '启用思考', value: true },
  { title: '禁用思考', value: false }
]

const thinkingLevelsList = [
  { key: 'off', label: '关闭' },
  { key: 'minimal', label: '最低' },
  { key: 'low', label: '低' },
  { key: 'medium', label: '中' },
  { key: 'high', label: '高' },
  { key: 'xhigh', label: '极高' },
  { key: 'max', label: '最高' }
]

const thinkingLevelsSettings = [
  { title: '关闭', value: 'off' },
  { title: '最低', value: 'minimal' },
  { title: '低', value: 'low' },
  { title: '中', value: 'medium' },
  { title: '高', value: 'high' },
  { title: '极高', value: 'xhigh' },
  { title: '最高', value: 'max' }
]

// ---- 编辑模型接口 ----
interface EditableModel {
  localId: string
  id: string
  name: string
  api: string
  baseUrl: string
  reasoning: boolean | null
  contextWindow: number | null
  maxTokens: number | null
  costInput: number | null
  costOutput: number | null
  costCacheRead: number | null
  costCacheWrite: number | null
  tlEnabled: Record<string, boolean>
  tlBudget: Record<string, string>
  inputText: string
  inputImage: boolean
  headersText: string
  compatText: string
}

interface EditableProvider {
  localId: string
  persisted: boolean
  id: string
  name: string
  api: string
  baseUrl: string
  apiKey: string
  authHeader: boolean | null
  headersText: string
  compatText: string
  modelOverridesText: string
  models: EditableModel[]
  hasOAuth: boolean
  apiKeyMasked: string
  apiKeyPresent: boolean
}

const providers = ref<EditableProvider[]>([])
const selectedProviderId = ref('')
const providersRevision = ref('')
const savingProvider = ref(false)
const testing = ref(false)
const discovering = ref(false)

const selectedProvider = computed(() => providers.value.find(p => p.localId === selectedProviderId.value) ?? null)
const isExistingProvider = computed(() => Boolean(selectedProvider.value?.persisted))
const localID = () => `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

// 安全字符串：任何 undefined/null 都转为 ''
const s = (v: unknown): string => (v == null ? '' : String(v))

// 安全 JSON 化
const toJSON = (v: unknown): string => {
  if (!v || typeof v !== 'object') return ''
  const keys = Object.keys(v as Record<string, unknown>)
  return keys.length ? JSON.stringify(v, null, 2) : ''
}

// 安全 JSON 解析
const parseJSON = (text: string, field: string): Record<string, unknown> => {
  if (!text.trim()) return {}
  let parsed: unknown
  try { parsed = JSON.parse(text) } catch { throw new Error(`${field} 必须是有效的 JSON 对象`) }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error(`${field} 必须是 JSON 对象`)
  return parsed as Record<string, unknown>
}

// 后端模型 → 编辑模型
const toEditableModel = (m: Record<string, unknown>): EditableModel => {
  const tlMap = (m.thinkingLevelMap as Record<string, string | null>) ?? {}
  const tlEnabled: Record<string, boolean> = {}
  const tlBudget: Record<string, string> = {}
  for (const { key } of thinkingLevelsList) {
    const val = tlMap[key]
    tlEnabled[key] = val !== undefined && val !== null
    tlBudget[key] = val ?? ''
  }
  const cost = m.cost as Record<string, number> | undefined
  const input = (m.input as string[]) ?? []
  return {
    localId: localID(),
    id: s(m.id),
    name: s(m.name),
    api: s(m.api),
    baseUrl: s(m.baseUrl),
    reasoning: (m.reasoning as boolean | null) ?? null,
    contextWindow: (m.contextWindow as number | null) ?? null,
    maxTokens: (m.maxTokens as number | null) ?? null,
    costInput: cost?.input ?? null,
    costOutput: cost?.output ?? null,
    costCacheRead: cost?.cacheRead ?? null,
    costCacheWrite: cost?.cacheWrite ?? null,
    tlEnabled, tlBudget,
    inputText: input.filter(v => v !== 'image').join(', '),
    inputImage: input.includes('image'),
    headersText: toJSON(m.headers),
    compatText: toJSON(m.compat)
  }
}

// 后端 provider → 编辑 provider
const toEditableProvider = (p: PiAgentProvider): EditableProvider => {
  const models = ((p.models as unknown as Record<string, unknown>[]) ?? []).map(toEditableModel)
  return {
    localId: localID(),
    persisted: true,
    id: s(p.id),
    name: s(p.name),
    api: s(p.api),
    baseUrl: s(p.baseUrl),
    apiKey: '',
    authHeader: (p.authHeader as boolean | null) ?? null,
    headersText: toJSON(p.headers),
    compatText: toJSON(p.compat),
    modelOverridesText: toJSON(p.modelOverrides),
    models,
    hasOAuth: (p.hasOAuth as boolean) ?? false,
    apiKeyMasked: s(p.apiKeyMasked),
    apiKeyPresent: (p.apiKeyPresent as boolean) ?? false
  }
}

// 编辑模型 → 后端模型对象
const toModelObj = (m: EditableModel): Record<string, unknown> => {
  const obj: Record<string, unknown> = {}
  obj.id = m.id
  if (m.name) obj.name = m.name
  if (m.api) obj.api = m.api
  if (m.baseUrl) obj.baseUrl = m.baseUrl
  if (m.reasoning !== null) obj.reasoning = m.reasoning
  if (m.contextWindow !== null) obj.contextWindow = m.contextWindow
  if (m.maxTokens !== null) obj.maxTokens = m.maxTokens
  const cost = { input: m.costInput, output: m.costOutput, cacheRead: m.costCacheRead, cacheWrite: m.costCacheWrite }
  if (Object.values(cost).some(v => v !== null && v !== 0)) obj.cost = cost
  const tlMap: Record<string, string | null> = {}
  for (const { key } of thinkingLevelsList) {
    if (m.tlEnabled[key]) tlMap[key] = m.tlBudget[key] || null
  }
  if (Object.keys(tlMap).length) obj.thinkingLevelMap = tlMap
  const input: string[] = []
  m.inputText.split(',').map(s => s.trim()).filter(Boolean).forEach(v => { if (v !== 'image') input.push(v) })
  if (m.inputImage) input.push('image')
  if (input.length) obj.input = input
  const h = parseJSON(m.headersText, 'headers'); if (Object.keys(h).length) obj.headers = h
  const c = parseJSON(m.compatText, 'compat'); if (Object.keys(c).length) obj.compat = c
  return obj
}

// 编辑 provider → 后端 provider 对象
const toProviderObj = (p: EditableProvider): Record<string, unknown> => {
  const obj: Record<string, unknown> = {}
  if (p.name) obj.name = p.name
  if (p.api) obj.api = p.api
  if (p.baseUrl) obj.baseUrl = p.baseUrl
  if (p.apiKey) obj.apiKey = p.apiKey
  if (p.authHeader !== null) obj.authHeader = p.authHeader
  const h = parseJSON(p.headersText, 'headers'); if (Object.keys(h).length) obj.headers = h
  const c = parseJSON(p.compatText, 'compat'); if (Object.keys(c).length) obj.compat = c
  const o = parseJSON(p.modelOverridesText, 'modelOverrides'); if (Object.keys(o).length) obj.modelOverrides = o
  const blankModel = p.models.find(m => !m.id.trim())
  if (blankModel) throw new Error('模型 ID 不能为空；请填写或删除空模型')
  const models = p.models.map(toModelObj)
  if (models.length) obj.models = models
  return obj
}

const selectProvider = (id: string) => { selectedProviderId.value = id }

const addProvider = () => {
  const draft: EditableProvider = {
    localId: localID(), persisted: false, id: '', name: '', api: '', baseUrl: '', apiKey: '',
    authHeader: null, headersText: '', compatText: '', modelOverridesText: '',
    models: [], hasOAuth: false, apiKeyMasked: '', apiKeyPresent: false
  }
  providers.value.push(draft)
  selectedProviderId.value = draft.localId
}

const makeDefaultTL = () => {
  const tlEnabled: Record<string, boolean> = {}
  const tlBudget: Record<string, string> = {}
  for (const { key } of thinkingLevelsList) { tlEnabled[key] = false; tlBudget[key] = '' }
  return { tlEnabled, tlBudget }
}

const addModel = () => {
  if (!selectedProvider.value) return
  selectedProvider.value.models.push({
    localId: localID(), id: '', name: '', api: '', baseUrl: '',
    reasoning: null, contextWindow: null, maxTokens: null,
    costInput: null, costOutput: null, costCacheRead: null, costCacheWrite: null,
    ...makeDefaultTL(), inputText: '', inputImage: false, headersText: '', compatText: ''
  })
}

const removeModel = (index: number) => {
  if (!selectedProvider.value) return
  selectedProvider.value.models.splice(index, 1)
}

const saveSelectedProvider = async () => {
  const provider = selectedProvider.value
  if (!provider) return
  savingProvider.value = true
  try {
    if (!provider.id.trim()) throw new Error('供应商标识不能为空')
    const providerObj = toProviderObj(provider)
    const existing = providers.value.find(p => p.id === provider.id && p.localId !== provider.localId)
    if (existing) throw new Error(`供应商 ${provider.id} 已存在`)
    if (provider.persisted) {
      const result = await api.updatePiAgentProvider(provider.id, { revision: providersRevision.value, provider: providerObj })
      providersRevision.value = result.revision
      notice.value = { visible: true, type: 'success', message: `已更新供应商 ${provider.id}` }
    } else {
      const result = await api.createPiAgentProvider({ id: provider.id.trim(), revision: providersRevision.value, provider: providerObj })
      providersRevision.value = result.revision
      notice.value = { visible: true, type: 'success', message: `已创建供应商 ${provider.id}` }
    }
    await loadProviders()
    selectedProviderId.value = providers.value.find(p => p.id === provider.id)?.localId ?? ''
  } catch (error) {
    handleApiError(error, '保存供应商失败')
  } finally {
    savingProvider.value = false
  }
}

const removeProvider = async (id: string) => {
  const provider = providers.value.find(p => p.id === id)
  if (!provider || !window.confirm(`删除供应商“${provider.name || provider.id}”及其模型？`)) return
  try {
    const result = await api.deletePiAgentProvider(id, providersRevision.value)
    providersRevision.value = result.revision
    notice.value = { visible: true, type: 'success', message: `已删除供应商 ${id}` }
    await loadProviders()
    selectedProviderId.value = providers.value[0]?.localId ?? ''
  } catch (error) {
    handleApiError(error, '删除供应商失败')
  }
}

const testProvider = async () => {
  const provider = selectedProvider.value
  if (!provider) return
  testing.value = true
  try {
    if (!provider.baseUrl.trim()) throw new Error('请先填写 Base URL')
    const result = await api.testPiAgentProvider(provider.id, { baseUrl: provider.baseUrl.trim(), apiKey: provider.apiKey || undefined, api: provider.api || undefined })
    if (result.success) {
      notice.value = { visible: true, type: 'success', message: `连通性测试通过（${result.latencyMs}ms，HTTP ${result.statusCode}）` }
    } else {
      notice.value = { visible: true, type: 'error', message: result.error || '连通性测试失败' }
    }
  } catch (error) {
    handleApiError(error, '连通性测试失败')
  } finally {
    testing.value = false
  }
}

const discoverModels = async () => {
  const provider = selectedProvider.value
  if (!provider) return
  discovering.value = true
  try {
    if (!provider.baseUrl.trim()) throw new Error('请先填写 Base URL')
    const result = await api.discoverPiAgentModels(provider.id, { baseUrl: provider.baseUrl.trim(), apiKey: provider.apiKey || undefined, api: provider.api || undefined })
    if (result.success && result.models?.length) {
      const existing = new Set(provider.models.map(m => m.id).filter(Boolean))
      for (const modelID of result.models) {
        if (existing.has(modelID)) continue
        provider.models.push({
          localId: localID(), id: modelID, name: '', api: '', baseUrl: '',
          reasoning: null, contextWindow: null, maxTokens: null,
          costInput: null, costOutput: null, costCacheRead: null, costCacheWrite: null,
          ...makeDefaultTL(), inputText: '', inputImage: false, headersText: '', compatText: ''
        })
        existing.add(modelID)
      }
      notice.value = { visible: true, type: 'success', message: `发现 ${result.models.length} 个模型候选，已加入列表（未保存）` }
    } else {
      notice.value = { visible: true, type: 'error', message: result.error || '未发现任何模型' }
    }
  } catch (error) {
    handleApiError(error, '模型发现失败')
  } finally {
    discovering.value = false
  }
}

// ---- 默认模型设置 ----
const modelSettings = ref({ defaultProvider: '', defaultModel: '', defaultThinkingLevel: '', enabledModels: [] as string[] })
const settingsRevision = ref('')
const savingSettings = ref(false)

const saveModelSettings = async () => {
  savingSettings.value = true
  try {
    const result = await api.updatePiAgentModelSettings({
      revision: settingsRevision.value,
      settings: {
        defaultProvider: modelSettings.value.defaultProvider || undefined,
        defaultModel: modelSettings.value.defaultModel || undefined,
        defaultThinkingLevel: modelSettings.value.defaultThinkingLevel || undefined,
        enabledModels: modelSettings.value.enabledModels
      }
    })
    settingsRevision.value = result.revision
    notice.value = { visible: true, type: 'success', message: '默认模型设置已保存' }
  } catch (error) {
    handleApiError(error, '保存默认模型设置失败')
  } finally {
    savingSettings.value = false
  }
}

// ---- 凭据 ----
const credentials = ref<PiAgentCredential[]>([])
const credentialsRevision = ref('')
const savingCredential = ref(false)
const credentialDialog = ref({ visible: false, credential: null as PiAgentCredential | null, key: '' })

const loadCredentials = async () => {
  try {
    const result = await api.getPiAgentCredentials()
    credentials.value = result.credentials
    credentialsRevision.value = result.revision
  } catch (error) { handleApiError(error, '加载凭据失败') }
}

const openCredentialDialog = (credential: PiAgentCredential) => { credentialDialog.value = { visible: true, credential, key: '' } }

const submitCredential = async (action: 'replace' | 'remove') => {
  const credential = credentialDialog.value.credential
  if (!credential) return
  savingCredential.value = true
  try {
    await api.updatePiAgentCredential(credential.id, { revision: credentialsRevision.value, action, key: credentialDialog.value.key || undefined })
    notice.value = { visible: true, type: 'success', message: action === 'replace' ? '密钥已更新' : '密钥已移除' }
    credentialDialog.value.visible = false
    await loadCredentials()
  } catch (error) { handleApiError(error, action === 'replace' ? '更新密钥失败' : '移除密钥失败') }
  finally { savingCredential.value = false }
}

const removeCredential = async (credential: PiAgentCredential) => {
  if (!window.confirm(`退出 ${credential.id} 的 OAuth 会话？`)) return
  try {
    await api.deletePiAgentCredential(credential.id, credentialsRevision.value)
    notice.value = { visible: true, type: 'success', message: `已退出 ${credential.id} 的 OAuth 会话` }
    await loadCredentials()
  } catch (error) { handleApiError(error, '退出 OAuth 会话失败') }
}

const credentialTypeLabel = (type: string) => ({ api_key: 'API Key', oauth: 'OAuth', unknown: '未知' })[type] ?? '未知'
const credentialTypeColor = (type: string) => ({ api_key: 'success', oauth: 'info', unknown: 'grey' })[type] ?? 'grey'

// ---- 备份 ----
const backupKinds: Array<{ label: string; value: PiAgentFileKind }> = [
  { label: 'models.json', value: 'models.json' }, { label: 'auth.json', value: 'auth.json' }, { label: 'settings.json', value: 'settings.json' }
]
const backups = ref<PiAgentBackup[]>([])
const creatingBackup = ref<PiAgentFileKind | ''>('')

const loadBackups = async () => {
  try {
    const result = await api.getPiAgentBackups()
    backups.value = result.backups
  } catch (error) { handleApiError(error, '加载备份失败') }
}

const createBackup = async (kind: PiAgentFileKind) => {
  creatingBackup.value = kind
  try {
    const result = await api.createPiAgentBackup(kind)
    notice.value = { visible: true, type: 'success', message: `已创建 ${kind} 备份` }
    backups.value.unshift(result.backup)
  } catch (error) { handleApiError(error, '创建备份失败') }
  finally { creatingBackup.value = '' }
}

const restoreBackup = async (backup: PiAgentBackup) => {
  if (!window.confirm(`确认从备份 ${backup.id} 恢复 ${backup.file}？`)) return
  try {
    await api.restorePiAgentBackup(backup.id)
    notice.value = { visible: true, type: 'success', message: `已恢复 ${backup.file}` }
    await loadAll(true)
  } catch (error) { handleApiError(error, '恢复备份失败') }
}

// ---- 通用 ----
const handleApiError = (error: unknown, fallback: string) => {
  const apiError = error as ApiError
  if (apiError?.status === 403) {
    disabled.value = true
    notice.value = { visible: true, type: 'error', message: apiError.message || 'pi-agent 配置管理未启用' }
    return
  }
  if (apiError?.status === 409 || apiError?.status === 428) {
    notice.value = { visible: true, type: 'error', message: apiError.status === 428 ? '保存前需要先读取最新配置，已刷新，请重试' : '配置文件已被外部修改，已刷新，请重试' }
    loadAll(true)
    return
  }
  notice.value = { visible: true, type: 'error', message: apiError?.message || fallback }
}

const formatTime = (value: string) => {
  if (!value) return '-'
  try { return new Date(value).toLocaleString() } catch { return value }
}
const formatSize = (bytes: number) => {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
const shortRevision = (revision: string) => revision ? `${revision.slice(0, 8)}…` : '-'

const loadStatus = async (): Promise<boolean> => {
  try {
    status.value = await api.getPiAgentStatus()
    disabled.value = false
    return true
  } catch (error) { handleApiError(error, '加载 pi-agent 状态失败') }
  return false
}

const loadProviders = async () => {
  try {
    const result = await api.getPiAgentProviders()
    providersRevision.value = result.revision
    const prevSelectionId = selectedProvider.value?.id ?? ''
    providers.value = result.providers.map(toEditableProvider)
    const target = prevSelectionId ? providers.value.find(p => p.id === prevSelectionId) : undefined
    selectedProviderId.value = target?.localId ?? providers.value[0]?.localId ?? ''
  } catch (error) { handleApiError(error, '加载供应商失败') }
}

const loadModelSettings = async () => {
  try {
    const result = await api.getPiAgentModelSettings()
    settingsRevision.value = result.revision
    modelSettings.value = {
      defaultProvider: result.settings.defaultProvider ?? '',
      defaultModel: result.settings.defaultModel ?? '',
      defaultThinkingLevel: result.settings.defaultThinkingLevel ?? '',
      enabledModels: result.settings.enabledModels ?? []
    }
  } catch (error) { handleApiError(error, '加载默认模型设置失败') }
}

const loadAll = async (silent = false) => {
  if (!silent) loading.value = true
  loadError.value = ''
  try {
    if (!await loadStatus() || disabled.value) return
    await Promise.all([loadProviders(), loadCredentials(), loadModelSettings(), loadBackups()])
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : '加载 pi-agent 配置失败'
  } finally {
    loading.value = false
  }
}

onMounted(() => loadAll())
</script>

<style scoped>
.mono-text { font-family: 'JetBrains Mono', 'Cascadia Code', monospace; }
</style>
