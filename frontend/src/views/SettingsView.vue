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
        <v-list nav density="compact" bg-color="transparent">
          <v-list-item active color="primary" prepend-icon="mdi-web" title="网络" />
        </v-list>
      </nav>

      <main class="settings-content">
        <section aria-labelledby="network-settings-title">
          <div class="section-heading">
            <div>
              <h2 id="network-settings-title" class="text-h6 font-weight-bold">网络</h2>
              <p class="text-body-2 text-medium-emphasis mt-1 mb-0">配置服务访问上游渠道时使用的默认代理</p>
            </div>
          </div>

          <v-alert v-if="loadError" type="error" variant="tonal" density="compact" class="mb-5">
            {{ loadError }}
          </v-alert>

          <v-skeleton-loader v-if="loading" type="list-item-two-line, list-item-two-line" />

          <v-form v-else ref="formRef" @submit.prevent="saveSettings">
            <div class="setting-row">
              <div class="setting-copy">
                <div class="text-body-1 font-weight-medium">全局上游代理</div>
                <div class="text-body-2 text-medium-emphasis mt-1">
                  启用后，未单独指定代理策略的渠道默认使用此地址
                </div>
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
                  hint="支持 http、https、socks5 和 socks5h；可在地址中包含用户名和密码"
                  persistent-hint
                />
              </div>
            </v-expand-transition>

            <v-alert v-if="notice" :type="notice.type" variant="tonal" density="compact" class="mt-5">
              {{ notice.message }}
            </v-alert>

            <div class="settings-actions">
              <v-btn type="submit" color="primary" :loading="saving" :disabled="!dirty">
                保存设置
              </v-btn>
            </div>
          </v-form>
        </section>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '@/services/api'

const formRef = ref()
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')
const proxyEnabled = ref(false)
const proxyUrl = ref('')
const savedProxyUrl = ref('')
const notice = ref<{ type: 'success' | 'error'; message: string } | null>(null)

const normalizedProxyUrl = computed(() => (proxyEnabled.value ? proxyUrl.value.trim() : ''))
const dirty = computed(() => normalizedProxyUrl.value !== savedProxyUrl.value)

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
    savedProxyUrl.value = current
    proxyUrl.value = current
    proxyEnabled.value = current !== ''
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : '加载设置失败'
  } finally {
    loading.value = false
  }
}

const saveSettings = async () => {
  if (proxyEnabled.value) {
    const { valid } = await formRef.value.validate()
    if (!valid) return
  }

  saving.value = true
  notice.value = null
  try {
    const settings = await api.updateSettings({
      network: { upstreamProxyUrl: normalizedProxyUrl.value }
    })
    const saved = settings.network?.upstreamProxyUrl?.trim() || ''
    savedProxyUrl.value = saved
    proxyUrl.value = saved
    proxyEnabled.value = saved !== ''
    notice.value = { type: 'success', message: '设置已保存' }
  } catch (error) {
    notice.value = { type: 'error', message: error instanceof Error ? error.message : '保存设置失败' }
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
  max-width: 720px;
  padding: 4px 0 48px;
}

.section-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
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

.settings-actions {
  display: flex;
  justify-content: flex-end;
  padding-top: 28px;
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

  .setting-row {
    align-items: flex-start;
  }
}
</style>
