<template>
  <div class="settings-page">
    <header class="settings-header">
      <div>
        <h1 class="text-h5 font-weight-bold">设置</h1>
        <p class="text-body-2 text-medium-emphasis mt-1 mb-0">管理服务级配置</p>
      </div>
    </header>

    <v-divider />

    <div class="settings-layout">
      <nav class="settings-nav" aria-label="设置分类">
        <v-list v-model:selected="selectedSections" nav mandatory density="compact" bg-color="transparent">
          <v-list-item value="network" color="primary" prepend-icon="mdi-web" title="网络" />
          <v-list-item value="content-safety" color="primary" prepend-icon="mdi-shield-alert" title="内容安全" />
        </v-list>
      </nav>

      <main class="settings-content">
        <v-alert v-if="loadError" type="error" variant="tonal" density="compact" class="mb-5">
          {{ loadError }}
        </v-alert>

        <v-skeleton-loader v-if="loading" type="list-item-two-line, list-item-two-line, list-item-two-line" />

        <section v-else-if="activeSection === 'network'" aria-labelledby="network-settings-title">
          <div class="section-heading">
            <div>
              <h2 id="network-settings-title" class="text-h6 font-weight-bold">网络</h2>
              <p class="text-body-2 text-medium-emphasis mt-1 mb-0">配置服务访问上游渠道时使用的默认代理</p>
            </div>
          </div>

          <v-form ref="formRef" @submit.prevent="saveNetworkSettings">
            <div class="setting-row">
              <div class="setting-copy">
                <div class="text-body-1 font-weight-medium">全局上游代理</div>
                <div class="text-body-2 text-medium-emphasis mt-1">未单独指定代理策略的渠道使用此地址</div>
              </div>
              <v-switch v-model="proxyEnabled" color="primary" inset hide-details aria-label="启用全局上游代理" />
            </div>

            <v-expand-transition>
              <div v-if="proxyEnabled" class="proxy-editor">
                <v-text-field
                  v-model="proxyUrl"
                  label="代理地址"
                  placeholder="http://127.0.0.1:7897"
                  prepend-inner-icon="mdi-web"
                  variant="outlined"
                  density="comfortable"
                  autocomplete="off"
                  spellcheck="false"
                  :rules="[proxyUrlRule]"
                  hint="支持 http、https、socks5 和 socks5h"
                  persistent-hint
                />
              </div>
            </v-expand-transition>

            <v-alert v-if="notice" :type="notice.type" variant="tonal" density="compact" class="mt-5">
              {{ notice.message }}
            </v-alert>

            <div class="settings-actions">
              <v-btn type="submit" color="primary" prepend-icon="mdi-content-save" :loading="saving" :disabled="!networkDirty">
                保存网络设置
              </v-btn>
            </div>
          </v-form>
        </section>

        <section v-else aria-labelledby="content-safety-settings-title">
          <div class="section-heading">
            <div>
              <h2 id="content-safety-settings-title" class="text-h6 font-weight-bold">内容安全</h2>
              <p class="text-body-2 text-medium-emphasis mt-1 mb-0">管理请求检测、信息掩码和响应拦截策略</p>
            </div>
            <v-btn variant="text" prepend-icon="mdi-format-list-bulleted" to="/blocked-logs">拦截记录</v-btn>
          </div>

          <form @submit.prevent="saveContentSafetySettings">
            <div class="safety-group">
              <div class="setting-row safety-master-row">
                <div class="setting-copy">
                  <div class="text-body-1 font-weight-medium">敏感词检测</div>
                  <div class="text-body-2 text-medium-emphasis mt-1">请求前检查用户输入</div>
                </div>
                <v-switch
                  v-model="contentSafety.sensitiveWord.enabled"
                  color="primary"
                  inset
                  hide-details
                  aria-label="启用敏感词检测"
                />
              </div>
              <div class="option-grid" :class="{ 'options-disabled': !contentSafety.sensitiveWord.enabled }">
                <v-switch
                  v-for="item in sensitiveWordCategories"
                  :key="item.key"
                  v-model="contentSafety.sensitiveWord[item.key]"
                  :label="item.label"
                  :disabled="!contentSafety.sensitiveWord.enabled"
                  density="compact"
                  color="primary"
                  inset
                  hide-details
                />
              </div>
            </div>

            <div class="safety-group">
              <div class="setting-row safety-master-row">
                <div class="setting-copy">
                  <div class="text-body-1 font-weight-medium">个人信息保护</div>
                  <div class="text-body-2 text-medium-emphasis mt-1">手机号、身份证、邮箱和 IP 地址</div>
                </div>
                <v-switch
                  v-model="contentSafety.sensitiveInfo.enabled"
                  color="primary"
                  inset
                  hide-details
                  aria-label="启用个人信息保护"
                />
              </div>
              <v-btn-toggle
                v-model="contentSafety.sensitiveInfo.mode"
                mandatory
                divided
                density="compact"
                class="mode-toggle"
                :disabled="!contentSafety.sensitiveInfo.enabled"
              >
                <v-btn value="audit">审计</v-btn>
                <v-btn value="block">阻断</v-btn>
                <v-btn value="mask">掩码</v-btn>
              </v-btn-toggle>
              <div class="option-grid" :class="{ 'options-disabled': !contentSafety.sensitiveInfo.enabled }">
                <v-checkbox
                  v-for="item in sensitiveInfoRules"
                  :key="item.value"
                  v-model="contentSafety.sensitiveInfo.enabledRules"
                  :value="item.value"
                  :label="item.label"
                  :disabled="!contentSafety.sensitiveInfo.enabled"
                  density="compact"
                  color="primary"
                  hide-details
                />
              </div>
            </div>

            <div class="safety-group">
              <div class="setting-row safety-master-row">
                <div class="setting-copy">
                  <div class="text-body-1 font-weight-medium">凭据保护</div>
                  <div class="text-body-2 text-medium-emphasis mt-1">区分用户输入与工具读取结果</div>
                </div>
                <v-switch
                  v-model="contentSafety.credential.enabled"
                  color="error"
                  inset
                  hide-details
                  aria-label="启用凭据保护"
                />
              </div>
              <div class="mode-settings" :class="{ 'options-disabled': !contentSafety.credential.enabled }">
                <div class="mode-setting-row">
                  <span class="text-body-2">用户与系统输入</span>
                  <v-btn-toggle
                    v-model="contentSafety.credential.userInputMode"
                    mandatory
                    divided
                    density="compact"
                    :disabled="!contentSafety.credential.enabled"
                  >
                    <v-btn value="audit">审计</v-btn>
                    <v-btn value="block">阻断</v-btn>
                    <v-btn value="mask">掩码</v-btn>
                  </v-btn-toggle>
                </div>
                <div class="mode-setting-row">
                  <span class="text-body-2">工具读取结果</span>
                  <v-btn-toggle
                    v-model="contentSafety.credential.toolResultMode"
                    mandatory
                    divided
                    density="compact"
                    :disabled="!contentSafety.credential.enabled"
                  >
                    <v-btn value="audit">审计</v-btn>
                    <v-btn value="block">阻断</v-btn>
                  </v-btn-toggle>
                </div>
                <div class="mode-setting-row">
                  <span class="text-body-2">模型生成的工具参数</span>
                  <v-btn-toggle
                    v-model="contentSafety.credential.toolArgumentMode"
                    mandatory
                    divided
                    density="compact"
                    :disabled="!contentSafety.credential.enabled"
                  >
                    <v-btn value="audit">审计</v-btn>
                    <v-btn value="block">阻断</v-btn>
                  </v-btn-toggle>
                </div>
              </div>
              <div class="option-grid" :class="{ 'options-disabled': !contentSafety.credential.enabled }">
                <v-checkbox
                  v-for="item in credentialRules"
                  :key="item.value"
                  v-model="contentSafety.credential.enabledRules"
                  :value="item.value"
                  :label="item.label"
                  :disabled="!contentSafety.credential.enabled"
                  density="compact"
                  color="error"
                  hide-details
                />
              </div>
            </div>

            <div class="safety-group">
              <div class="setting-row safety-master-row">
                <div class="setting-copy">
                  <div class="text-body-1 font-weight-medium">危险命令检测</div>
                  <div class="text-body-2 text-medium-emphasis mt-1">响应代码块和工具参数</div>
                </div>
                <v-switch
                  v-model="contentSafety.dangerousCmd.enabled"
                  color="warning"
                  inset
                  hide-details
                  aria-label="启用危险命令检测"
                />
              </div>
              <div class="option-grid" :class="{ 'options-disabled': !contentSafety.dangerousCmd.enabled }">
                <v-checkbox
                  v-for="item in dangerousCommandRules"
                  :key="item.value"
                  v-model="contentSafety.dangerousCmd.enabledRules"
                  :value="item.value"
                  :label="item.label"
                  :disabled="!contentSafety.dangerousCmd.enabled"
                  density="compact"
                  color="warning"
                  hide-details
                />
              </div>
            </div>

            <div class="safety-group">
              <div class="setting-row safety-master-row">
                <div class="setting-copy">
                  <div class="text-body-1 font-weight-medium">白名单</div>
                  <div class="text-body-2 text-medium-emphasis mt-1">
                    命中工具名的 tool_result 与 tool_argument 跳过全部检测，并在拦截记录里留审计条目
                  </div>
                </div>
                <v-switch
                  v-model="contentSafety.whitelist.enabled"
                  color="success"
                  inset
                  hide-details
                  aria-label="启用白名单放行"
                />
              </div>
              <v-combobox
                v-model="contentSafety.whitelist.toolNames"
                :disabled="!contentSafety.whitelist.enabled"
                label="放行工具名"
                placeholder="例如 tavily-search、tavily_search"
                multiple
                chips
                closable-chips
                variant="outlined"
                density="comfortable"
                hide-details="auto"
                class="mt-3"
                hint="回车添加，每个 chip 是一个工具名；匹配区分大小写"
                persistent-hint
              />
            </div>

            <v-alert v-if="notice" :type="notice.type" variant="tonal" density="compact" class="mt-5">
              {{ notice.message }}
            </v-alert>

            <div class="settings-actions">
              <v-btn
                type="submit"
                color="primary"
                prepend-icon="mdi-content-save"
                :loading="saving"
                :disabled="!contentSafetyDirty"
              >
                保存内容安全设置
              </v-btn>
            </div>
          </form>
        </section>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  api,
  type ContentSafetySettings,
  type CredentialRule,
  type DangerousCommandRule,
  type SensitiveInfoRule
} from '@/services/api'

type SettingsSection = 'network' | 'content-safety'
type Notice = { type: 'success' | 'error'; message: string } | null
type SensitiveWordToggle =
  | 'pornographyEnabled'
  | 'gamblingEnabled'
  | 'drugsEnabled'
  | 'violenceTerrorEnabled'
  | 'politicalEnabled'
  | 'illegalCrimeEnabled'

const sensitiveWordCategories: Array<{ key: SensitiveWordToggle; label: string }> = [
  { key: 'pornographyEnabled', label: '色情' },
  { key: 'gamblingEnabled', label: '赌博' },
  { key: 'drugsEnabled', label: '毒品' },
  { key: 'violenceTerrorEnabled', label: '暴力与恐怖' },
  { key: 'politicalEnabled', label: '政治敏感' },
  { key: 'illegalCrimeEnabled', label: '违法犯罪' }
]

const sensitiveInfoRules: Array<{ value: SensitiveInfoRule; label: string }> = [
  { value: 'phone', label: '手机号' },
  { value: 'id_card', label: '身份证号' },
  { value: 'email', label: '邮箱' },
  { value: 'ip_address', label: 'IP 地址' }
]

const credentialRules: Array<{ value: CredentialRule; label: string }> = [
  { value: 'api_key', label: '已知 API Key / Token' },
  { value: 'named_secret', label: '密码与命名密钥' },
  { value: 'private_key', label: 'PEM 私钥' },
  { value: 'connection_string', label: '数据库连接串' },
  { value: 'high_entropy', label: '高熵环境变量值' }
]

const dangerousCommandRules: Array<{ value: DangerousCommandRule; label: string }> = [
  { value: 'destructive', label: '破坏性命令' },
  { value: 'download_execute', label: '远程下载执行' },
  { value: 'reverse_shell', label: '反弹 Shell' },
  { value: 'privilege_escalation', label: '权限提升' },
  { value: 'environment_tampering', label: '环境变量篡改' }
]

const defaultContentSafety = (): ContentSafetySettings => ({
  sensitiveWord: {
    enabled: false,
    pornographyEnabled: false,
    gamblingEnabled: false,
    drugsEnabled: false,
    violenceTerrorEnabled: false,
    politicalEnabled: false,
    illegalCrimeEnabled: false,
    customWords: []
  },
  sensitiveInfo: {
    enabled: false,
    mode: 'mask',
    enabledRules: []
  },
  credential: {
    enabled: false,
    userInputMode: 'block',
    toolResultMode: 'block',
    toolArgumentMode: 'block',
    enabledRules: []
  },
  dangerousCmd: {
    enabled: false,
    enabledRules: []
  },
  whitelist: {
    enabled: false,
    toolNames: []
  }
})

const normalizeContentSafety = (
  settings: ContentSafetySettings | null | undefined
): ContentSafetySettings => {
  const defaults = defaultContentSafety()
  if (!settings) return defaults

  const sensitiveWord = settings.sensitiveWord
  const sensitiveInfo = settings.sensitiveInfo
  const credential = settings.credential
  const dangerousCmd = settings.dangerousCmd

  const rawInfoRules = Array.isArray(sensitiveInfo?.enabledRules)
    ? (sensitiveInfo.enabledRules as string[])
    : []
  const legacyAPIKey = rawInfoRules.includes('api_key')
  const normalizedInfoRules = rawInfoRules.filter(rule => rule !== 'api_key') as SensitiveInfoRule[]

  return {
    sensitiveWord: {
      ...defaults.sensitiveWord,
      ...(sensitiveWord || {}),
      customWords: Array.isArray(sensitiveWord?.customWords) ? [...sensitiveWord.customWords] : []
    },
    sensitiveInfo: {
      ...defaults.sensitiveInfo,
      ...(sensitiveInfo || {}),
      enabled: legacyAPIKey && normalizedInfoRules.length === 0 ? false : Boolean(sensitiveInfo?.enabled),
      enabledRules: normalizedInfoRules
    },
    credential: {
      ...defaults.credential,
      ...(credential || {}),
      enabled: credential?.enabled ?? (legacyAPIKey && Boolean(sensitiveInfo?.enabled)),
      userInputMode: credential?.userInputMode ?? (legacyAPIKey ? 'mask' : defaults.credential.userInputMode),
      toolResultMode: credential?.toolResultMode ?? defaults.credential.toolResultMode,
      toolArgumentMode: credential?.toolArgumentMode ?? defaults.credential.toolArgumentMode,
      enabledRules: Array.isArray(credential?.enabledRules)
        ? [...credential.enabledRules]
        : legacyAPIKey
          ? ['api_key']
          : []
    },
    dangerousCmd: {
      ...defaults.dangerousCmd,
      ...(dangerousCmd || {}),
      enabledRules: Array.isArray(dangerousCmd?.enabledRules)
        ? [...dangerousCmd.enabledRules]
        : dangerousCmd
          ? []
          : [...defaults.dangerousCmd.enabledRules]
    },
    whitelist: {
      ...defaults.whitelist,
      ...(settings.whitelist || {}),
      toolNames: Array.isArray(settings.whitelist?.toolNames) ? [...settings.whitelist.toolNames] : []
    }
  }
}

const formRef = ref()
const selectedSections = ref<SettingsSection[]>(['network'])
const activeSection = computed<SettingsSection>(() => selectedSections.value[0] || 'network')
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const proxyEnabled = ref(false)
const proxyUrl = ref('')
const savedProxyUrl = ref('')
const savedProxyEnabled = ref(false)
const contentSafety = ref<ContentSafetySettings>(defaultContentSafety())
const savedContentSafety = ref('')
const notice = ref<Notice>(null)

const normalizedProxyUrl = computed(() => (proxyEnabled.value ? proxyUrl.value.trim() : ''))
const networkDirty = computed(() =>
  proxyEnabled.value !== savedProxyEnabled.value || normalizedProxyUrl.value !== savedProxyUrl.value
)
const contentSafetyDirty = computed(() => JSON.stringify(contentSafety.value) !== savedContentSafety.value)

const proxyUrlRule = (value: string) => {
  const raw = value?.trim()
  if (!raw) return '请输入代理地址'
  try {
    const parsed = new URL(raw)
    if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(parsed.protocol)) {
      return '仅支持 http、https、socks5 和 socks5h 协议'
    }
    if (!parsed.hostname) return '代理地址必须包含主机'
    if ((parsed.pathname && parsed.pathname !== '/') || parsed.search || parsed.hash) {
      return '代理地址不能包含路径、查询参数或片段'
    }
    return true
  } catch {
    return '请输入有效的代理地址'
  }
}

const loadSettings = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const settings = await api.getSettings()
    const current = settings.network?.upstreamProxyUrl?.trim() || ''
    const enabled = settings.network?.upstreamProxyEnabled ?? false
    savedProxyUrl.value = current
    savedProxyEnabled.value = enabled
    proxyUrl.value = current
    proxyEnabled.value = enabled
    contentSafety.value = normalizeContentSafety(settings.contentSafety)
    savedContentSafety.value = JSON.stringify(contentSafety.value)
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : '加载设置失败'
  } finally {
    loading.value = false
  }
}

const saveNetworkSettings = async () => {
  if (proxyEnabled.value) {
    const { valid } = await formRef.value.validate()
    if (!valid) return
  }

  saving.value = true
  notice.value = null
  try {
    const settings = await api.updateSettings({
      network: {
        // 提交规范化值：开关关闭时同步清空地址，避免后端残留旧代理地址强制生效
        upstreamProxyUrl: normalizedProxyUrl.value,
        upstreamProxyEnabled: proxyEnabled.value
      }
    })
    const saved = settings.network?.upstreamProxyUrl?.trim() || ''
    const enabled = settings.network?.upstreamProxyEnabled ?? false
    savedProxyUrl.value = saved
    savedProxyEnabled.value = enabled
    proxyUrl.value = saved
    proxyEnabled.value = enabled
    notice.value = { type: 'success', message: '网络设置已保存' }
  } catch (error) {
    notice.value = { type: 'error', message: error instanceof Error ? error.message : '保存网络设置失败' }
  } finally {
    saving.value = false
  }
}

const saveContentSafetySettings = async () => {
  notice.value = null
  const settingsToSave = normalizeContentSafety(contentSafety.value)
  const hasSensitiveWordRule = sensitiveWordCategories.some(item => settingsToSave.sensitiveWord[item.key]) ||
    settingsToSave.sensitiveWord.customWords.length > 0
  if (settingsToSave.sensitiveWord.enabled && !hasSensitiveWordRule) {
    notice.value = { type: 'error', message: '敏感词检测已启用，但未选择任何分类或自定义词' }
    return
  }
  for (const [enabled, rules, label] of [
    [settingsToSave.sensitiveInfo.enabled, settingsToSave.sensitiveInfo.enabledRules, '个人信息保护'],
    [settingsToSave.credential.enabled, settingsToSave.credential.enabledRules, '凭据保护'],
    [settingsToSave.dangerousCmd.enabled, settingsToSave.dangerousCmd.enabledRules, '危险命令检测']
  ] as const) {
    if (enabled && rules.length === 0) {
      notice.value = { type: 'error', message: `${label}已启用，但未选择任何规则` }
      return
    }
  }

  saving.value = true
  try {
    const settings = await api.updateSettings({ contentSafety: settingsToSave })
    contentSafety.value = normalizeContentSafety(settings.contentSafety)
    savedContentSafety.value = JSON.stringify(contentSafety.value)
    notice.value = { type: 'success', message: '内容安全设置已保存' }
  } catch (error) {
    notice.value = { type: 'error', message: error instanceof Error ? error.message : '保存内容安全设置失败' }
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>

<style scoped>
.settings-page {
  max-width: 1120px;
  margin: 0 auto;
}

.settings-header {
  display: flex;
  align-items: center;
  min-height: 84px;
  padding: 8px 4px 20px;
}

.settings-layout {
  display: grid;
  grid-template-columns: minmax(180px, 220px) minmax(0, 1fr);
  gap: 40px;
  padding-top: 24px;
}

.settings-nav {
  border-right: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  padding-right: 20px;
}

.settings-content {
  max-width: 760px;
  min-width: 0;
  padding: 4px 0 48px;
}

.section-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 28px;
}

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 32px;
  min-height: 72px;
  padding: 16px 0;
  border-top: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
}

.setting-copy {
  min-width: 0;
}

.proxy-editor {
  padding-top: 24px;
}

.safety-group + .safety-group {
  margin-top: 28px;
}

.safety-master-row {
  border-bottom: 0;
}

.mode-toggle {
  margin: 0 0 12px;
}

.mode-settings {
  display: grid;
  gap: 10px;
  margin-bottom: 12px;
}

.mode-setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.option-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 2px 24px;
  min-height: 104px;
  padding: 10px 16px 12px;
  border: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
  border-radius: 6px;
  background: rgba(var(--v-theme-surface-variant), 0.24);
  transition: opacity 180ms ease;
}

.options-disabled {
  opacity: 0.58;
}

.settings-actions {
  display: flex;
  justify-content: flex-end;
  padding-top: 28px;
}

@media (prefers-reduced-motion: reduce) {
  .option-grid {
    transition: none;
  }
}

@media (max-width: 700px) {
  .settings-layout {
    grid-template-columns: 1fr;
    gap: 20px;
  }

  .settings-nav {
    border-right: 0;
    border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity));
    padding-right: 0;
    padding-bottom: 12px;
  }

  .settings-nav :deep(.v-list) {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .setting-row {
    align-items: flex-start;
    gap: 20px;
  }

  .mode-setting-row {
    align-items: flex-start;
    flex-direction: column;
  }

  .section-heading {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (max-width: 480px) {
  .option-grid {
    grid-template-columns: 1fr;
  }
}
</style>
