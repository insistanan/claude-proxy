<template>
  <div class="agent-config-page gpt-page">
    <AgentConfigHeader title="GPT 配置" subtitle="管理当前服务运行用户的 Codex CLI 代理与 API Key">
      <template #default>
        <v-btn variant="text" prepend-icon="mdi-refresh" :loading="loading" @click="loadSettings">刷新</v-btn>
        <v-btn color="primary" prepend-icon="mdi-content-save" :loading="saving" :disabled="loading" @click="saveSettings">保存配置</v-btn>
      </template>
    </AgentConfigHeader>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-5" closable @click:close="error = ''">{{ error }}</v-alert>
    <v-progress-linear v-if="loading" indeterminate color="primary" class="mb-5" />

    <template v-else>
      <AgentConfigLocation
        :label="settings?.configExists ? 'Codex 配置文件' : '首次保存将创建 Codex 配置文件'"
        :path="settings?.configPath"
      />
      <AgentConfigLocation
        :label="settings?.authExists ? 'Codex 凭据文件' : '设置 API Key 后将创建凭据文件'"
        :path="settings?.authPath"
        class="mb-5"
      />

      <v-card elevation="0" class="settings-card agent-config-panel">
        <div class="agent-config-panel-header">
          <div>
            <div class="agent-config-panel-title">代理连接</div>
            <div class="agent-config-panel-subtitle">仅修改所选 model provider 的 Base URL，不会切换 config.toml 的 model_provider。</div>
          </div>
        </div>
        <v-divider />
        <v-card-text class="pa-5">
          <section class="agent-config-callout mb-4 channel-quick-pick">
            <div class="agent-config-callout__title">
              <v-icon size="18">mdi-lightning-bolt</v-icon>
              连接本代理
            </div>
            <div>
              <v-btn color="primary" variant="tonal" prepend-icon="mdi-link-variant" :loading="connecting" @click="connectToProxy">
                一键连接本代理（{{ defaultBaseUrl || 'http://localhost:.../v1' }}）
              </v-btn>
              <div class="text-caption text-medium-emphasis mt-2">Codex 固定走 Responses 协议，点击后自动填充 Base URL 与当前代理访问密钥。</div>
            </div>
          </section>

          <v-divider class="my-4" />

          <v-combobox
            v-model="provider"
            :items="providerOptions"
            item-title="title"
            item-value="value"
            :return-object="false"
            label="Model Provider"
            variant="outlined"
            density="comfortable"
            placeholder="选择已有 provider，或输入 provider 名称"
            hint="对应 config.toml 中的 [model_providers.&lt;provider&gt;]；名称支持字母、数字、下划线和连字符，不会修改根级 model_provider"
            persistent-hint
          >
            <template #item="{ props, item }">
              <v-list-item v-bind="props" :subtitle="item.raw.subtitle" />
            </template>
          </v-combobox>

          <v-alert v-if="settings?.activeProvider" type="info" variant="tonal" density="compact" class="mt-3 mb-4">
            当前 model_provider：<strong>{{ settings.activeProvider }}</strong>
          </v-alert>

          <v-text-field
            v-model.trim="baseUrl"
            label="Base URL"
            variant="outlined"
            density="comfortable"
            placeholder="例如 http://localhost:9996/v1"
            hint="写入所选 [model_providers.&lt;provider&gt;] 下的 base_url；留空会删除该字段"
            persistent-hint
          />

          <v-text-field
            v-model="apiKey"
            label="OPENAI_API_KEY"
            type="password"
            variant="outlined"
            density="comfortable"
            class="mt-4"
            :placeholder="apiKeyPresent ? `当前：${apiKeyMasked}（留空则保留）` : '输入 API Key'"
            @update:model-value="onAPIKeyInput"
          >
            <template #append-inner>
              <v-btn v-if="apiKeyPresent" icon="mdi-delete" size="x-small" variant="text" title="清除已保存 API Key" @click="clearAPIKey" />
            </template>
          </v-text-field>
        </v-card-text>
      </v-card>
    </template>

    <v-snackbar v-model="notice.visible" :color="notice.type" location="top right" :timeout="3500">{{ notice.message }}</v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AgentConfigHeader from '@/components/AgentConfigHeader.vue'
import AgentConfigLocation from '@/components/AgentConfigLocation.vue'
import { fetchProxyBaseUrl } from '@/composables/useChannelQuickPick'
import { api, type CodexSettings } from '@/services/api'
import { useAuthStore } from '@/stores/auth'

const loading = ref(false)
const saving = ref(false)
const connecting = ref(false)
const error = ref('')
const settings = ref<CodexSettings | null>(null)
const provider = ref('')
const baseUrl = ref('')
const apiKey = ref('')
const apiKeyMasked = ref('')
const apiKeyPresent = ref(false)
const originalAPIKeyPresent = ref(false)
const apiKeyDeleteRequested = ref(false)
const apiKeyAction = ref<'keep' | 'replace' | 'remove'>('keep')
const defaultBaseUrl = ref('')
const notice = ref({ visible: false, type: 'success', message: '' })

const providerOptions = computed(() => (settings.value?.providers ?? []).map(item => ({
  title: item.name ? `${item.id}（${item.name}）` : item.id,
  value: item.id,
  subtitle: item.baseUrl || '未配置 Base URL'
})))

const selectedProvider = computed(() => settings.value?.providers.find(item => item.id === provider.value))

watch(selectedProvider, selected => {
  baseUrl.value = selected?.baseUrl ?? ''
})

const proxyResponsesBaseUrl = async () => `${(await fetchProxyBaseUrl()).replace(/\/$/, '')}/v1`

const loadSettings = async () => {
  loading.value = true
  error.value = ''
  try {
    const loaded = await api.getCodexSettings()
    settings.value = loaded
    provider.value = loaded.selectedProvider
    baseUrl.value = loaded.providers.find(item => item.id === loaded.selectedProvider)?.baseUrl ?? ''
    apiKey.value = ''
    apiKeyMasked.value = loaded.apiKeyMasked
    apiKeyPresent.value = loaded.apiKeyPresent
    originalAPIKeyPresent.value = loaded.apiKeyPresent
    apiKeyDeleteRequested.value = false
    apiKeyAction.value = 'keep'
    defaultBaseUrl.value = await proxyResponsesBaseUrl()
  } catch (loadError) {
    error.value = loadError instanceof Error ? loadError.message : '加载 GPT 配置失败'
  } finally {
    loading.value = false
  }
}

const connectToProxy = async () => {
  connecting.value = true
  try {
    baseUrl.value = await proxyResponsesBaseUrl()
    defaultBaseUrl.value = baseUrl.value
    const authStore = useAuthStore()
    if (authStore.apiKey) {
      apiKey.value = authStore.apiKey
      apiKeyPresent.value = true
      apiKeyDeleteRequested.value = false
      apiKeyAction.value = 'replace'
    }
    notice.value = { visible: true, type: 'success', message: `已填入本代理地址：${baseUrl.value}` }
  } catch (connectError) {
    notice.value = { visible: true, type: 'error', message: connectError instanceof Error ? connectError.message : '获取本代理地址失败' }
  } finally {
    connecting.value = false
  }
}

const onAPIKeyInput = () => {
  if (apiKey.value) {
    apiKeyDeleteRequested.value = false
    apiKeyPresent.value = true
    apiKeyAction.value = 'replace'
    return
  }
  apiKeyPresent.value = originalAPIKeyPresent.value && !apiKeyDeleteRequested.value
  apiKeyAction.value = apiKeyDeleteRequested.value ? 'remove' : 'keep'
}

const clearAPIKey = () => {
  apiKey.value = ''
  apiKeyMasked.value = ''
  apiKeyPresent.value = false
  apiKeyDeleteRequested.value = true
  apiKeyAction.value = 'remove'
}

const saveSettings = async () => {
  const providerID = provider.value.trim()
  if (!providerID) {
    notice.value = { visible: true, type: 'error', message: '请选择或输入 Model Provider' }
    return
  }
  saving.value = true
  try {
    const response = await api.saveCodexSettings({
      provider: providerID,
      baseUrl: baseUrl.value,
      apiKeyAction: apiKeyAction.value,
      apiKey: apiKey.value
    })
    notice.value = { visible: true, type: 'success', message: `已保存到 ${response.configPath}` }
    await loadSettings()
  } catch (saveError) {
    notice.value = { visible: true, type: 'error', message: saveError instanceof Error ? saveError.message : '保存 GPT 配置失败' }
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>
