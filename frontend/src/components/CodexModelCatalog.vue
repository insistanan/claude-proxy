<template>
  <v-card elevation="0" class="settings-card agent-config-panel">
    <div class="agent-config-panel-header">
      <div>
        <div class="agent-config-panel-title">模型目录</div>
        <div class="agent-config-panel-subtitle">
          管理 Codex 模型元数据（models.json），Codex 依据 input_modalities 等字段判断模型能力；预设模板与 DeepSeek 官方接入文档一致。
        </div>
      </div>
      <v-btn color="primary" variant="tonal" prepend-icon="mdi-plus" @click="openCreateForm">新增模型</v-btn>
    </div>
    <v-divider />
    <v-card-text class="pa-5">
      <v-alert v-if="settings?.modelsError" type="warning" variant="tonal" density="compact" class="mb-4">
        models.json 解析失败：{{ settings.modelsError }}。请先修复该文件后再操作模型目录。
      </v-alert>

      <section class="agent-config-callout mb-4">
        <div class="agent-config-callout__title">
          <v-icon size="18">mdi-lightning-bolt</v-icon>
          默认模型
        </div>
        <v-row>
          <v-col cols="12" sm="5">
            <v-select
              v-model="defaultModel"
              :items="modelOptions"
              item-title="title"
              item-value="value"
              label="model"
              variant="outlined"
              density="comfortable"
              :disabled="!models.length"
              hint="写入 config.toml 根级 model 与 model_catalog_json"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" sm="4">
            <v-select
              v-model="defaultEffort"
              :items="effortOptions"
              label="推理强度"
              variant="outlined"
              density="comfortable"
              :disabled="!defaultModel"
              hint="选择“跟随模型默认”会移除 model_reasoning_effort"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" sm="3" class="d-flex align-center">
            <v-btn
              color="primary"
              prepend-icon="mdi-content-save"
              :loading="savingDefault"
              :disabled="!defaultModel"
              block
              @click="saveDefault"
            >
              写入 config.toml
            </v-btn>
          </v-col>
        </v-row>
        <div v-if="settings" class="text-caption text-medium-emphasis mt-2">
          当前 model：{{ settings.model || '（未设置）' }}
          <template v-if="settings.modelReasoningEffort">，model_reasoning_effort：{{ settings.modelReasoningEffort }}</template>
        </div>
      </section>

      <v-table v-if="models.length" density="comfortable">
        <thead>
          <tr>
            <th>模型</th>
            <th>输入</th>
            <th>上下文窗口</th>
            <th>推理档位</th>
            <th class="text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="model in models" :key="model.slug">
            <td>
              <div class="d-flex align-center ga-2">
                <span class="font-weight-medium">{{ model.slug }}</span>
                <v-chip v-if="settings?.model === model.slug" size="x-small" color="primary" variant="tonal">默认</v-chip>
              </div>
              <div class="text-caption text-medium-emphasis">
                {{ model.displayName }}<template v-if="model.description"> · {{ model.description }}</template>
              </div>
            </td>
            <td>
              <div class="d-flex ga-1">
                <v-chip size="x-small" variant="tonal" prepend-icon="mdi-text">文本</v-chip>
                <v-chip v-if="model.supportsImage" size="x-small" variant="tonal" color="primary" prepend-icon="mdi-image">图片</v-chip>
              </div>
            </td>
            <td class="text-no-wrap">{{ formatContextWindow(model.contextWindow) }}</td>
            <td>
              <div>{{ model.defaultReasoningLevel }}</div>
              <div class="text-caption text-medium-emphasis">{{ model.reasoningLevels.join(' / ') }}</div>
            </td>
            <td class="text-right text-no-wrap">
              <v-btn icon="mdi-pencil" size="small" variant="text" title="编辑" @click="openEditForm(model)" />
              <v-btn
                icon="mdi-delete"
                size="small"
                variant="text"
                color="error"
                title="删除"
                :loading="deletingSlug === model.slug"
                @click="deleteModel(model)"
              />
            </td>
          </tr>
        </tbody>
      </v-table>
      <v-alert v-else type="info" variant="tonal" density="compact">
        尚未登记模型。新增模型后即可在 Codex 中选用，并通过上方“默认模型”写入 config.toml。
      </v-alert>

      <template v-if="form.visible">
        <v-divider class="my-4" />
        <div class="agent-config-panel-title mb-3">{{ form.editing ? `编辑模型：${form.slug}` : '新增模型' }}</div>
        <v-row>
          <v-col v-if="!form.editing" cols="12" sm="6">
            <v-select
              v-model="form.template"
              :items="templateOptions"
              item-title="title"
              item-value="value"
              label="预设模板"
              variant="outlined"
              density="comfortable"
              hint="决定元数据固定字段（apply_patch_tool_type 等），可调字段见下方"
              persistent-hint
              @update:model-value="applyTemplate"
            >
              <template #item="{ props: itemProps, item }">
                <v-list-item v-bind="itemProps" :subtitle="item.raw.subtitle" />
              </template>
            </v-select>
          </v-col>
          <v-col cols="12" sm="6">
            <v-text-field
              v-if="!form.editing"
              v-model.trim="form.slug"
              label="Slug"
              variant="outlined"
              density="comfortable"
              placeholder="例如 my-deepseek"
              hint="模型标识（models.json 的 slug），支持字母、数字、点、下划线和连字符"
              persistent-hint
            />
            <v-text-field v-else v-model="form.slug" label="Slug" variant="outlined" density="comfortable" disabled />
          </v-col>
          <v-col cols="12" sm="6">
            <v-text-field v-model.trim="form.displayName" label="显示名称" variant="outlined" density="comfortable" placeholder="留空则使用 slug" />
          </v-col>
          <v-col cols="12" sm="6">
            <v-text-field v-model.trim="form.description" label="描述" variant="outlined" density="comfortable" placeholder="留空则沿用原值（新增时使用模板描述）" />
          </v-col>
          <v-col cols="12" sm="4">
            <v-switch
              v-model="form.supportsImage"
              color="primary"
              label="图片输入"
              hint="对应 input_modalities 与 supports_image_detail_original"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" sm="4">
            <v-text-field
              v-model.number="form.contextWindow"
              type="number"
              label="上下文窗口"
              variant="outlined"
              density="comfortable"
              min="1"
              hint="同时写入 context_window 与 max_context_window"
              persistent-hint
            />
          </v-col>
          <v-col cols="12" sm="4">
            <v-select
              v-model="form.defaultReasoningLevel"
              :items="formReasoningLevels"
              label="默认推理档位"
              variant="outlined"
              density="comfortable"
              :disabled="!formReasoningLevels.length"
            />
          </v-col>
        </v-row>
        <div class="d-flex justify-end ga-2">
          <v-btn variant="text" @click="closeForm">取消</v-btn>
          <v-btn color="primary" prepend-icon="mdi-content-save" :loading="savingModel" :disabled="!formValid" @click="saveModel">
            {{ form.editing ? '保存修改' : '新增模型' }}
          </v-btn>
        </div>
      </template>
    </v-card-text>
  </v-card>
  <v-snackbar v-model="notice.visible" :color="notice.type" location="top right" :timeout="3500">{{ notice.message }}</v-snackbar>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { api, type CodexCatalogModel, type CodexSettings, type SaveCodexModelCatalog } from '@/services/api'

const props = defineProps<{ settings: CodexSettings | null }>()
const emit = defineEmits<{ saved: [] }>()

const models = computed(() => props.settings?.catalogModels ?? [])
const templates = computed(() => props.settings?.modelTemplates ?? [])

const defaultModel = ref('')
const defaultEffort = ref('')
const savingDefault = ref(false)
const deletingSlug = ref('')
const savingModel = ref(false)
const notice = ref({ visible: false, type: 'success', message: '' })

const form = reactive({
  visible: false,
  editing: false,
  template: '',
  slug: '',
  displayName: '',
  description: '',
  supportsImage: false,
  contextWindow: 128000,
  defaultReasoningLevel: ''
})

const slugPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/

const modelOptions = computed(() =>
  models.value.map(model => ({
    title: model.displayName ? `${model.slug}（${model.displayName}）` : model.slug,
    value: model.slug
  }))
)

const selectedDefaultModel = computed(() => models.value.find(model => model.slug === defaultModel.value))

const effortOptions = computed(() => [
  { title: '跟随模型默认', value: '' },
  ...(selectedDefaultModel.value?.reasoningLevels ?? []).map(level => ({ title: level, value: level }))
])

const templateOptions = computed(() =>
  templates.value.map(template => ({
    title: template.name,
    value: template.id,
    subtitle: template.supportsImage ? `${template.description} · 支持图片` : template.description
  }))
)

const selectedFormTemplate = computed(() => templates.value.find(template => template.id === form.template))

const formReasoningLevels = computed(() => {
  if (form.editing) return models.value.find(model => model.slug === form.slug)?.reasoningLevels ?? []
  return selectedFormTemplate.value?.reasoningLevels.map(level => level.effort) ?? []
})

const formValid = computed(() => {
  if (!slugPattern.test(form.slug)) return false
  if (!form.editing && !form.template) return false
  if (!form.contextWindow || form.contextWindow <= 0) return false
  return form.defaultReasoningLevel !== ''
})

watch(defaultModel, slug => {
  const model = models.value.find(item => item.slug === slug)
  if (model && !model.reasoningLevels.includes(defaultEffort.value)) {
    defaultEffort.value = ''
  }
})

watch(
  () => props.settings,
  settings => {
    if (!settings) return
    defaultModel.value = models.value.some(model => model.slug === settings.model) ? settings.model : ''
    const current = models.value.find(model => model.slug === settings.model)
    defaultEffort.value = current?.reasoningLevels.includes(settings.modelReasoningEffort)
      ? settings.modelReasoningEffort
      : ''
  },
  { immediate: true }
)

const applyTemplate = (templateId: string) => {
  const template = templates.value.find(item => item.id === templateId)
  if (!template) return
  form.supportsImage = template.supportsImage
  form.contextWindow = template.contextWindow
  form.defaultReasoningLevel = template.defaultReasoningLevel
  form.description = template.description
}

const openCreateForm = () => {
  form.visible = true
  form.editing = false
  form.template = templates.value[0]?.id ?? ''
  form.slug = ''
  form.displayName = ''
  const template = templates.value[0]
  form.description = template?.description ?? ''
  form.supportsImage = template?.supportsImage ?? false
  form.contextWindow = template?.contextWindow ?? 128000
  form.defaultReasoningLevel = template?.defaultReasoningLevel ?? ''
}

const openEditForm = (model: CodexCatalogModel) => {
  form.visible = true
  form.editing = true
  form.template = ''
  form.slug = model.slug
  form.displayName = model.displayName
  form.description = model.description
  form.supportsImage = model.supportsImage
  form.contextWindow = model.contextWindow
  form.defaultReasoningLevel = model.defaultReasoningLevel
}

const closeForm = () => {
  form.visible = false
}

const saveModel = async () => {
  savingModel.value = true
  try {
    const payload: SaveCodexModelCatalog = {
      action: 'upsert',
      slug: form.slug,
      displayName: form.displayName,
      description: form.description,
      supportsImage: form.supportsImage,
      contextWindow: form.contextWindow,
      defaultReasoningLevel: form.defaultReasoningLevel
    }
    if (!form.editing) payload.template = form.template
    const response = await api.saveCodexModelCatalog(payload)
    notice.value = { visible: true, type: 'success', message: `已保存到 ${response.modelsPath}` }
    closeForm()
    emit('saved')
  } catch (saveError) {
    notice.value = { visible: true, type: 'error', message: saveError instanceof Error ? saveError.message : '保存模型失败' }
  } finally {
    savingModel.value = false
  }
}

const saveDefault = async () => {
  if (!defaultModel.value) return
  savingDefault.value = true
  try {
    const payload: SaveCodexModelCatalog = { action: 'setDefault', slug: defaultModel.value }
    if (defaultEffort.value) {
      payload.reasoningEffort = defaultEffort.value
    } else if (props.settings?.modelReasoningEffort) {
      payload.clearReasoningEffort = true
    }
    const response = await api.saveCodexModelCatalog(payload)
    notice.value = { visible: true, type: 'success', message: `已写入 ${response.configPath}` }
    emit('saved')
  } catch (saveError) {
    notice.value = { visible: true, type: 'error', message: saveError instanceof Error ? saveError.message : '写入默认模型失败' }
  } finally {
    savingDefault.value = false
  }
}

const deleteModel = async (model: CodexCatalogModel) => {
  if (!window.confirm(`删除模型“${model.slug}”？config.toml 中的引用不会被修改。`)) return
  deletingSlug.value = model.slug
  try {
    const response = await api.saveCodexModelCatalog({ action: 'delete', slug: model.slug })
    notice.value = { visible: true, type: 'success', message: `已从 ${response.modelsPath} 删除` }
    if (form.visible && form.editing && form.slug === model.slug) closeForm()
    emit('saved')
  } catch (deleteError) {
    notice.value = { visible: true, type: 'error', message: deleteError instanceof Error ? deleteError.message : '删除模型失败' }
  } finally {
    deletingSlug.value = ''
  }
}

const formatContextWindow = (value: number) => {
  if (value >= 1048576 && value % 1048576 === 0) return `${value / 1048576}M`
  return value.toLocaleString()
}
</script>
