<template>
  <div class="opencode-page">
    <div class="page-heading mb-5">
      <div>
        <div class="text-h5 font-weight-bold">OpenCode 配置</div>
        <div class="text-body-2 text-medium-emphasis mt-1">管理当前服务运行用户的 OpenCode 模型提供商和默认模型</div>
      </div>
      <div class="d-flex align-center ga-2">
        <v-btn variant="text" prepend-icon="mdi-refresh" :loading="loading" @click="loadConfig">刷新</v-btn>
        <v-btn color="primary" prepend-icon="mdi-content-copy" :loading="saving" :disabled="loading" @click="saveConfig">保存配置</v-btn>
      </div>
    </div>

    <v-alert v-if="loadError" type="error" variant="tonal" class="mb-5" closable @click:close="loadError = ''">
      {{ loadError }}
    </v-alert>

    <v-progress-linear v-if="loading" indeterminate color="primary" class="mb-5" />

    <template v-else>
      <v-alert type="info" variant="tonal" density="compact" class="mb-5 config-path-alert">
        <div class="d-flex flex-wrap align-center ga-2">
          <span>{{ config?.exists ? '当前配置文件' : '首次保存将创建配置文件' }}</span>
          <code>{{ config?.path }}</code>
          <v-chip v-if="config?.jsonc" size="x-small" label>JSONC 将在保存后规范化为 JSON</v-chip>
        </div>
      </v-alert>

      <v-row>
        <v-col cols="12" lg="4">
          <v-card class="provider-list-card" elevation="0">
            <div class="d-flex align-center justify-space-between px-4 py-3">
              <div class="text-subtitle-1 font-weight-bold">提供商</div>
              <v-btn icon="mdi-plus" size="small" variant="text" title="添加提供商" @click="addProvider" />
            </div>
            <v-divider />
            <v-list v-if="providers.length" density="comfortable" nav class="py-2">
              <v-list-item
                v-for="provider in providers"
                :key="provider.id"
                :active="selectedProviderId === provider.id"
                :title="provider.name || provider.id"
                :subtitle="provider.id"
                rounded="sm"
                @click="selectedProviderId = provider.id"
              >
                <template #prepend>
                  <v-icon size="20">mdi-server-network</v-icon>
                </template>
                <template #append>
                  <v-chip size="x-small" :color="protocolColor(provider.protocol)" label>{{ protocolLabel(provider.protocol) }}</v-chip>
                </template>
              </v-list-item>
            </v-list>
            <div v-else class="empty-providers text-center text-medium-emphasis px-6 py-10">
              <v-icon size="32" class="mb-3">mdi-server-network</v-icon>
              <div class="text-body-2">尚未配置提供商</div>
              <v-btn class="mt-4" size="small" color="primary" variant="tonal" prepend-icon="mdi-plus" @click="addProvider">添加提供商</v-btn>
            </div>
          </v-card>
        </v-col>

        <v-col cols="12" lg="8">
          <v-card v-if="selectedProvider" elevation="0" class="provider-editor-card">
            <div class="d-flex align-center justify-space-between px-5 py-4">
              <div>
                <div class="text-subtitle-1 font-weight-bold">{{ selectedProvider.name || '新提供商' }}</div>
                <div class="text-caption text-medium-emphasis">配置会写入 OpenCode 标准 provider 字段</div>
              </div>
              <v-btn color="error" variant="text" size="small" prepend-icon="mdi-delete" @click="removeProvider(selectedProvider.id)">删除</v-btn>
            </div>
            <v-divider />

            <v-card-text class="pa-5">
              <v-row>
                <v-col cols="12" sm="6">
                  <v-text-field v-model.trim="selectedProvider.id" label="提供商标识" variant="outlined" density="comfortable" :disabled="providerIdInUse" hint="例如 deepseek；模型引用将使用 deepseek/模型标识" persistent-hint @update:model-value="selectedProviderId = $event" />
                </v-col>
                <v-col cols="12" sm="6">
                  <v-text-field v-model.trim="selectedProvider.name" label="显示名称" variant="outlined" density="comfortable" placeholder="例如 DeepSeek" />
                </v-col>
                <v-col cols="12" sm="6">
                  <v-select v-model="selectedProvider.protocol" label="协议" variant="outlined" density="comfortable" :items="protocols" item-title="title" item-value="value" @update:model-value="applyProtocolNpm(selectedProvider)" />
                </v-col>
                <v-col v-if="selectedProvider.protocol === 'custom'" cols="12" sm="6">
                  <v-text-field v-model.trim="selectedProvider.npm" label="AI SDK 包" variant="outlined" density="comfortable" placeholder="例如 @ai-sdk/provider" />
                </v-col>
                <v-col :cols="selectedProvider.protocol === 'custom' ? 12 : 6">
                  <v-text-field v-model.trim="selectedProvider.baseUrl" label="Base URL" variant="outlined" density="comfortable" placeholder="例如 http://127.0.0.1:8080/v1" hint="填入 API 版本根路径，不要填具体 endpoint" persistent-hint />
                </v-col>
                <v-col cols="12">
                  <v-text-field
                    v-model="selectedProvider.apiKey"
                    label="API Key"
                    type="password"
                    variant="outlined"
                    density="comfortable"
                    :placeholder="selectedProvider.apiKeyPresent ? `当前：${selectedProvider.apiKeyMasked}（留空则保留）` : '可直接填写，也可使用 {env:VARIABLE}'"
                    @update:model-value="onAPIKeyInput(selectedProvider)"
                  >
                    <template #append-inner>
                      <v-btn v-if="selectedProvider.apiKeyPresent" icon="mdi-delete" size="x-small" variant="text" title="清除已保存密钥" @click="clearAPIKey(selectedProvider)" />
                    </template>
                  </v-text-field>
                </v-col>
              </v-row>

              <v-expansion-panels variant="accordion" class="mt-2">
                <v-expansion-panel title="请求头与高级提供商选项">
                  <v-expansion-panel-text>
                    <v-row>
                      <v-col cols="12" md="6">
                        <v-textarea v-model="selectedProvider.headersText" label="请求头 JSON" variant="outlined" density="comfortable" rows="5" auto-grow placeholder="{&#10;  &quot;X-Custom-Header&quot;: &quot;value&quot;&#10;}" />
                      </v-col>
                      <v-col cols="12" md="6">
                        <v-textarea v-model="selectedProvider.optionsText" label="高级 provider options JSON" variant="outlined" density="comfortable" rows="5" auto-grow placeholder="{&#10;  &quot;timeout&quot;: 600000&#10;}" />
                      </v-col>
                    </v-row>
                  </v-expansion-panel-text>
                </v-expansion-panel>
              </v-expansion-panels>

              <div class="d-flex align-center justify-space-between mt-7 mb-3">
                <div>
                  <div class="text-subtitle-1 font-weight-bold">模型</div>
                  <div class="text-caption text-medium-emphasis">模型标识必须与上游 API 接收的模型名一致</div>
                </div>
                <v-btn size="small" color="primary" variant="tonal" prepend-icon="mdi-plus" @click="addModel(selectedProvider)">添加模型</v-btn>
              </div>

              <div v-if="selectedProvider.models.length === 0" class="empty-models text-body-2 text-medium-emphasis py-6 text-center">添加至少一个模型后，它才会出现在 OpenCode 的模型列表。</div>
              <v-expansion-panels v-else variant="accordion" class="model-panels">
                <v-expansion-panel v-for="model in selectedProvider.models" :key="model.localId">
                  <template #title>
                    <div class="d-flex align-center ga-2 overflow-hidden">
                      <span class="font-weight-medium text-truncate">{{ model.name || model.key || '新模型' }}</span>
                      <span v-if="model.key" class="text-caption text-medium-emphasis text-truncate">{{ model.key }}</span>
                    </div>
                  </template>
                  <v-expansion-panel-text>
                    <v-row>
                      <v-col cols="12" sm="6">
                        <v-text-field v-model.trim="model.key" label="模型标识" variant="outlined" density="comfortable" placeholder="例如 deepseek-v4-pro" />
                      </v-col>
                      <v-col cols="12" sm="6">
                        <v-text-field v-model.trim="model.name" label="显示名称" variant="outlined" density="comfortable" placeholder="例如 DeepSeek V4 Pro" />
                      </v-col>
                      <v-col cols="12">
                        <v-text-field v-model.trim="model.apiModelId" label="上游模型 ID（可选）" variant="outlined" density="comfortable" placeholder="留空时使用模型标识；用于上游名称与 OpenCode 名称不同的情况" />
                      </v-col>
                      <v-col cols="12" sm="4">
                        <v-text-field v-model.number="model.contextLimit" label="上下文长度" type="number" min="0" variant="outlined" density="comfortable" />
                      </v-col>
                      <v-col cols="12" sm="4">
                        <v-text-field v-model.number="model.inputLimit" label="最大输入 Token" type="number" min="0" variant="outlined" density="comfortable" />
                      </v-col>
                      <v-col cols="12" sm="4">
                        <v-text-field v-model.number="model.outputLimit" label="最大输出 Token" type="number" min="0" variant="outlined" density="comfortable" />
                      </v-col>
                      <v-col cols="12" sm="6">
                        <v-select v-model="model.reasoningEffort" label="思考等级" variant="outlined" density="comfortable" :items="reasoningEfforts" item-title="title" item-value="value" />
                      </v-col>
                      <v-col cols="12">
                        <v-textarea v-model="model.optionsText" label="模型高级 options JSON" variant="outlined" density="comfortable" rows="3" auto-grow placeholder="{&#10;  &quot;reasoningEffort&quot;: &quot;high&quot;&#10;}" />
                      </v-col>
                    </v-row>
                    <div class="d-flex justify-end">
                      <v-btn color="error" variant="text" size="small" prepend-icon="mdi-delete" @click="removeModel(selectedProvider, model.localId)">删除模型</v-btn>
                    </div>
                  </v-expansion-panel-text>
                </v-expansion-panel>
              </v-expansion-panels>
            </v-card-text>
          </v-card>

          <v-card v-else elevation="0" class="empty-editor d-flex align-center justify-center text-center pa-8">
            <div>
              <v-icon size="44" color="primary" class="mb-3">mdi-cog</v-icon>
              <div class="text-subtitle-1 font-weight-bold">选择或添加一个提供商</div>
              <div class="text-body-2 text-medium-emphasis mt-1">在这里配置 OpenCode 使用的协议、密钥和模型。</div>
            </div>
          </v-card>
        </v-col>
      </v-row>

    </template>

    <v-snackbar v-model="notice.visible" :color="notice.type" location="top right" :timeout="3500">{{ notice.message }}</v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type OpenCodeConfig, type OpenCodeProtocol, type OpenCodeProvider, type SaveOpenCodeProvider } from '@/services/api'

interface EditableModel {
  localId: string
  key: string
  apiModelId: string
  name: string
  contextLimit: number
  inputLimit: number
  outputLimit: number
  reasoningEffort: string
  optionsText: string
}

interface EditableProvider {
  id: string
  name: string
  protocol: OpenCodeProtocol
  npm: string
  baseUrl: string
  apiKeyMasked: string
  apiKeyPresent: boolean
  apiKey: string
  apiKeyAction: 'keep' | 'replace' | 'remove'
  headersText: string
  optionsText: string
  models: EditableModel[]
}

const protocols: Array<{ title: string; value: OpenCodeProtocol }> = [
  { title: 'Chat Completions', value: 'chat' },
  { title: 'Responses', value: 'responses' },
  { title: 'Messages', value: 'messages' },
  { title: '自定义 SDK', value: 'custom' }
]

const reasoningEfforts = [
  { title: '使用提供商默认值', value: '' },
  { title: '不启用推理', value: 'none' },
  { title: '低', value: 'low' },
  { title: '中', value: 'medium' },
  { title: '高', value: 'high' },
  { title: '超高', value: 'xhigh' }
]

const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const config = ref<OpenCodeConfig | null>(null)
const providers = ref<EditableProvider[]>([])
const selectedProviderId = ref('')
const notice = ref({ visible: false, type: 'success', message: '' })

const selectedProvider = computed(() => providers.value.find(provider => provider.id === selectedProviderId.value) ?? null)
const providerIdInUse = computed(() => Boolean(selectedProvider.value && config.value?.providers.some(provider => provider.id === selectedProvider.value?.id)))
const localID = () => `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

const stringifyJSON = (value: Record<string, unknown>) => Object.keys(value).length ? JSON.stringify(value, null, 2) : ''

const toEditableProvider = (provider: OpenCodeProvider): EditableProvider => ({
  ...provider,
  apiKey: '',
  apiKeyAction: 'keep',
  headersText: stringifyJSON(provider.headers),
  optionsText: stringifyJSON(provider.options),
  models: provider.models.map(model => {
    const options = { ...model.options }
    const reasoningEffort = typeof options.reasoningEffort === 'string' ? options.reasoningEffort : ''
    delete options.reasoningEffort
    return { ...model, localId: localID(), reasoningEffort, optionsText: stringifyJSON(options) }
  })
})

const loadConfig = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const loaded = await api.getOpenCodeConfig()
    config.value = loaded
    providers.value = loaded.providers.map(toEditableProvider)
    selectedProviderId.value = providers.value[0]?.id ?? ''
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : '加载 OpenCode 配置失败'
  } finally {
    loading.value = false
  }
}

const addProvider = () => {
  let index = providers.value.length + 1
  let id = `provider-${index}`
  while (providers.value.some(provider => provider.id === id)) {
    index += 1
    id = `provider-${index}`
  }
  providers.value.push({
    id,
    name: '',
    protocol: 'chat',
    npm: '@ai-sdk/openai-compatible',
    baseUrl: '',
    apiKeyMasked: '',
    apiKeyPresent: false,
    apiKey: '',
    apiKeyAction: 'keep',
    headersText: '',
    optionsText: '',
    models: []
  })
  selectedProviderId.value = id
}

const removeProvider = (id: string) => {
  const provider = providers.value.find(item => item.id === id)
  if (!provider || !window.confirm(`删除提供商“${provider.name || provider.id}”及其模型？`)) return
  providers.value = providers.value.filter(item => item !== provider)
  selectedProviderId.value = providers.value[0]?.id ?? ''
}

const addModel = (provider: EditableProvider) => {
  provider.models.push({
    localId: localID(),
    key: '',
    apiModelId: '',
    name: '',
    contextLimit: 200000,
    inputLimit: 80000,
    outputLimit: 64800,
    reasoningEffort: '',
    optionsText: ''
  })
}

const removeModel = (provider: EditableProvider, localId: string) => {
  provider.models = provider.models.filter(model => model.localId !== localId)
}

const applyProtocolNpm = (provider: EditableProvider) => {
  const npm: Record<Exclude<OpenCodeProtocol, 'custom'>, string> = {
    chat: '@ai-sdk/openai-compatible',
    responses: '@ai-sdk/openai',
    messages: '@ai-sdk/anthropic'
  }
  if (provider.protocol !== 'custom') provider.npm = npm[provider.protocol]
}

const protocolLabel = (protocol: OpenCodeProtocol) => protocols.find(item => item.value === protocol)?.title ?? '自定义 SDK'
const protocolColor = (protocol: OpenCodeProtocol) => ({ chat: 'info', responses: 'primary', messages: 'success', custom: 'warning' })[protocol]

const onAPIKeyInput = (provider: EditableProvider) => {
  if (provider.apiKey) provider.apiKeyAction = 'replace'
  else if (!provider.apiKeyPresent) provider.apiKeyAction = 'keep'
}

const clearAPIKey = (provider: EditableProvider) => {
  provider.apiKey = ''
  provider.apiKeyAction = 'remove'
  provider.apiKeyPresent = false
  provider.apiKeyMasked = ''
}

const parseJSONObject = (text: string, field: string): Record<string, unknown> => {
  if (!text.trim()) return {}
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    throw new Error(`${field} 必须是有效的 JSON 对象`)
  }
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error(`${field} 必须是 JSON 对象`)
  return parsed as Record<string, unknown>
}

const prepareProvider = (provider: EditableProvider): SaveOpenCodeProvider => {
  const headers = parseJSONObject(provider.headersText, `提供商 ${provider.id} 的请求头`)
  if (Object.values(headers).some(value => typeof value !== 'string')) throw new Error(`提供商 ${provider.id} 的请求头值必须是字符串`)
  return {
    id: provider.id.trim(),
    name: provider.name.trim(),
    protocol: provider.protocol,
    npm: provider.npm.trim(),
    baseUrl: provider.baseUrl.trim(),
    apiKeyAction: provider.apiKeyAction,
    apiKey: provider.apiKey,
    headers: headers as Record<string, string>,
    options: parseJSONObject(provider.optionsText, `提供商 ${provider.id} 的高级选项`),
    models: provider.models.map(model => {
      const options = parseJSONObject(model.optionsText, `模型 ${model.key || '新模型'} 的高级选项`)
      if (model.reasoningEffort) options.reasoningEffort = model.reasoningEffort
      else delete options.reasoningEffort
      return {
        key: model.key.trim(),
        apiModelId: model.apiModelId.trim(),
        name: model.name.trim(),
        contextLimit: Number(model.contextLimit) || 0,
        inputLimit: Number(model.inputLimit) || 0,
        outputLimit: Number(model.outputLimit) || 0,
        options
      }
    })
  }
}

const saveConfig = async () => {
  saving.value = true
  try {
    const payload = { providers: providers.value.map(prepareProvider) }
    const response = await api.saveOpenCodeConfig(payload)
    notice.value = { visible: true, type: 'success', message: `已保存到 ${response.path}` }
    await loadConfig()
  } catch (error) {
    notice.value = { visible: true, type: 'error', message: error instanceof Error ? error.message : '保存 OpenCode 配置失败' }
  } finally {
    saving.value = false
  }
}

onMounted(loadConfig)
</script>

<style scoped>
.opencode-page { max-width: 1480px; margin: 0 auto; }
.page-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.config-path-alert code { overflow-wrap: anywhere; }
.provider-list-card, .provider-editor-card, .default-model-card, .empty-editor { border: 1px solid rgba(var(--v-theme-on-surface), 0.12); }
.provider-list-card { min-height: 320px; }
.empty-editor { min-height: 390px; }
.empty-providers, .empty-models { border: 1px dashed rgba(var(--v-theme-on-surface), 0.2); border-radius: 6px; }
.model-panels :deep(.v-expansion-panel) { border: 1px solid rgba(var(--v-theme-on-surface), 0.12); margin-bottom: 8px; }
@media (max-width: 600px) { .page-heading { align-items: flex-start; flex-direction: column; } }
</style>
