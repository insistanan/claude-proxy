<template>
  <div class="claude-code-page">
    <div class="page-heading mb-5">
      <div>
        <div class="text-h5 font-weight-bold">Claude Code 配置</div>
        <div class="text-body-2 text-medium-emphasis mt-1">管理当前服务运行用户的 Claude Code 代理和模型映射</div>
      </div>
      <div class="d-flex align-center ga-2">
        <v-btn variant="text" prepend-icon="mdi-refresh" :loading="loading" @click="loadSettings">刷新</v-btn>
        <v-btn color="primary" prepend-icon="mdi-content-copy" :loading="saving" :disabled="loading" @click="saveSettings">保存配置</v-btn>
      </div>
    </div>

    <v-alert v-if="error" type="error" variant="tonal" class="mb-5" closable @click:close="error = ''">{{ error }}</v-alert>
    <v-progress-linear v-if="loading" indeterminate color="primary" class="mb-5" />

    <template v-else>
      <v-alert type="info" variant="tonal" density="compact" class="mb-5 config-path-alert">
        <div class="d-flex flex-wrap align-center ga-2">
          <span>{{ settings?.exists ? '当前配置文件' : '首次保存将创建配置文件' }}</span>
          <code>{{ settings?.path }}</code>
          <v-chip v-if="settings?.jsonc" size="x-small" label>JSONC 将在保存后规范化为 JSON</v-chip>
        </div>
      </v-alert>

      <v-row>
        <v-col cols="12" lg="7">
          <v-card elevation="0" class="settings-card h-100">
            <v-card-title class="px-5 pt-5 pb-2 text-subtitle-1 font-weight-bold">代理连接</v-card-title>
            <v-card-text class="pa-5">
              <v-text-field v-model.trim="baseUrl" label="Anthropic Base URL" variant="outlined" density="comfortable" placeholder="例如 http://127.0.0.1:8080" hint="留空则使用 Claude Code 默认 Anthropic 端点" persistent-hint />
              <v-radio-group v-model="credentialKind" inline class="mt-2 mb-1" hide-details>
                <v-radio label="认证令牌" value="authToken" />
                <v-radio label="API Key" value="apiKey" />
              </v-radio-group>
              <v-text-field
                v-model="credential"
                :label="credentialKind === 'authToken' ? 'ANTHROPIC_AUTH_TOKEN' : 'ANTHROPIC_API_KEY'"
                type="password"
                variant="outlined"
                density="comfortable"
                :placeholder="credentialPresent ? `当前：${credentialMasked}（留空则保留）` : '输入密钥'"
                @update:model-value="onCredentialInput"
              >
                <template #append-inner>
                  <v-btn v-if="credentialPresent" icon="mdi-delete" size="x-small" variant="text" title="清除已保存密钥" @click="clearCredential" />
                </template>
              </v-text-field>
            </v-card-text>
          </v-card>
        </v-col>

        <v-col cols="12" lg="5">
          <v-card elevation="0" class="settings-card h-100">
            <v-card-title class="px-5 pt-5 pb-2 text-subtitle-1 font-weight-bold">会话模型覆盖</v-card-title>
            <v-card-text class="pa-5">
              <v-text-field v-model.trim="model" label="主模型覆盖" variant="outlined" density="comfortable" placeholder="例如 sonnet 或完整模型 ID" hint="留空则使用 Claude Code 默认模型" persistent-hint />
              <v-text-field v-model.trim="reasoningModel" label="推理模型覆盖" variant="outlined" density="comfortable" placeholder="可选" hint="留空则保持 Claude Code 默认推理选择" persistent-hint />
            </v-card-text>
          </v-card>
        </v-col>
      </v-row>

      <v-card elevation="0" class="settings-card mt-5">
        <v-card-title class="px-5 pt-5 pb-1 text-subtitle-1 font-weight-bold">默认模型标识</v-card-title>
        <v-card-subtitle class="px-5 pb-3">为空时不会写入对应环境变量，Claude Code 将使用自身默认模型。</v-card-subtitle>
        <v-card-text class="pa-5 pt-2">
          <v-row v-for="item in modelDefaults" :key="item.family" class="model-default-row">
            <v-col cols="12" sm="3" class="d-flex align-center pt-sm-5">
              <v-chip :color="familyColor(item.family)" label>{{ familyTitle(item.family) }}</v-chip>
            </v-col>
            <v-col cols="12" sm="5">
              <v-text-field v-model.trim="item.model" label="模型标识" variant="outlined" density="comfortable" :placeholder="`${familyTitle(item.family)} 默认模型`" />
            </v-col>
            <v-col cols="12" sm="4">
              <v-text-field v-model.trim="item.name" label="显示名称" variant="outlined" density="comfortable" placeholder="可选" />
            </v-col>
          </v-row>
        </v-card-text>
      </v-card>
    </template>

    <v-snackbar v-model="notice.visible" :color="notice.type" location="top right" :timeout="3500">{{ notice.message }}</v-snackbar>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type ClaudeCodeModelDefault, type ClaudeCodeSettings } from '@/services/api'

const loading = ref(false)
const saving = ref(false)
const error = ref('')
const settings = ref<ClaudeCodeSettings | null>(null)
const baseUrl = ref('')
const credentialKind = ref<'authToken' | 'apiKey'>('authToken')
const credential = ref('')
const credentialMasked = ref('')
const credentialPresent = ref(false)
const credentialAction = ref<'keep' | 'replace' | 'remove'>('keep')
const model = ref('')
const reasoningModel = ref('')
const modelDefaults = ref<ClaudeCodeModelDefault[]>([])
const notice = ref({ visible: false, type: 'success', message: '' })

const familyTitles: Record<string, string> = { fable: 'Fable', opus: 'Opus', sonnet: 'Sonnet', haiku: 'Haiku' }
const familyColors: Record<string, string> = { fable: 'primary', opus: 'warning', sonnet: 'success', haiku: 'info' }
const familyTitle = (family: string) => familyTitles[family] ?? family
const familyColor = (family: string) => familyColors[family] ?? 'default'

const loadSettings = async () => {
  loading.value = true
  error.value = ''
  try {
    const loaded = await api.getClaudeCodeSettings()
    settings.value = loaded
    baseUrl.value = loaded.baseUrl
    credentialKind.value = loaded.credentialKind
    credential.value = ''
    credentialMasked.value = loaded.credentialMasked
    credentialPresent.value = loaded.credentialPresent
    credentialAction.value = 'keep'
    model.value = loaded.model
    reasoningModel.value = loaded.reasoningModel
    modelDefaults.value = loaded.modelDefaults.map(item => ({ ...item }))
  } catch (loadError) {
    error.value = loadError instanceof Error ? loadError.message : '加载 Claude Code 配置失败'
  } finally {
    loading.value = false
  }
}

const onCredentialInput = () => {
  if (credential.value) credentialAction.value = 'replace'
  else if (!credentialPresent.value) credentialAction.value = 'keep'
}

const clearCredential = () => {
  credential.value = ''
  credentialPresent.value = false
  credentialMasked.value = ''
  credentialAction.value = 'remove'
}

const saveSettings = async () => {
  saving.value = true
  try {
    const response = await api.saveClaudeCodeSettings({
      baseUrl: baseUrl.value,
      credentialKind: credentialKind.value,
      credentialAction: credentialAction.value,
      credential: credential.value,
      model: model.value,
      reasoningModel: reasoningModel.value,
      modelDefaults: modelDefaults.value
    })
    notice.value = { visible: true, type: 'success', message: `已保存到 ${response.path}` }
    await loadSettings()
  } catch (saveError) {
    notice.value = { visible: true, type: 'error', message: saveError instanceof Error ? saveError.message : '保存 Claude Code 配置失败' }
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>

<style scoped>
.claude-code-page { max-width: 1280px; margin: 0 auto; }
.page-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.settings-card { border: 1px solid rgba(var(--v-theme-on-surface), 0.12); }
.config-path-alert code { overflow-wrap: anywhere; }
.model-default-row + .model-default-row { border-top: 1px solid rgba(var(--v-theme-on-surface), 0.1); }
@media (max-width: 600px) { .page-heading { align-items: flex-start; flex-direction: column; } }
</style>
