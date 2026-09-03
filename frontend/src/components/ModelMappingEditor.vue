<template>
  <v-card variant="outlined" rounded="lg">
    <v-card-title class="d-flex align-center justify-space-between pa-4 pb-2">
      <div class="d-flex align-center ga-2">
        <v-icon color="primary">mdi-swap-horizontal</v-icon>
        <span class="text-body-1 font-weight-bold">模型重定向 (可选)</span>
      </div>
      <v-chip size="small" color="secondary" variant="tonal"> 自动转换模型名称 </v-chip>
    </v-card-title>

    <v-card-text class="pt-2">
      <div class="text-body-2 text-medium-emphasis mb-4">
        {{ hint }}
        <div class="text-caption mt-1">目标模型按列表顺序尝试；拖动左侧手柄可调整优先级。</div>
      </div>

      <!-- 现有映射列表 -->
      <div v-if="Object.keys(modelValue).length" class="mb-4">
        <v-list density="compact" class="bg-transparent">
          <v-list-item
            v-for="[source, targets] in Object.entries(modelValue)"
            :key="source"
            class="mb-2"
            rounded="lg"
            variant="tonal"
            color="surface-variant"
          >
            <template #prepend>
              <v-icon size="small" color="primary">mdi-arrow-right</v-icon>
            </template>
            <v-list-item-title>
              <div class="d-flex flex-column ga-1">
                <div class="d-flex align-center ga-2">
                  <code class="text-caption font-weight-bold">{{ source }}</code>
                  <v-icon size="small" color="primary">mdi-arrow-right</v-icon>
                  <v-chip size="x-small" color="info" variant="tonal">{{ targets.length }} 个备选</v-chip>
                </div>
                <draggable
                  :list="targets"
                  :item-key="getTargetKey"
                  handle=".mapping-target-drag-handle"
                  class="d-flex flex-wrap ga-1 mt-1"
                  :animation="150"
                  ghost-class="mapping-target-ghost"
                  @change="reorderTargets(source, targets)"
                >
                  <template #item="{ element: target }">
                    <v-chip
                      size="x-small"
                      closable
                      @click:close.stop="removeTarget(source, target)"
                    >
                      <v-icon size="12" class="mapping-target-drag-handle mr-1" title="拖拽调整优先级">
                        mdi-drag-vertical
                      </v-icon>
                      <code class="text-caption">{{ target }}</code>
                    </v-chip>
                  </template>
                </draggable>
              </div>
            </v-list-item-title>
            <template #append>
              <v-btn size="small" color="error" icon variant="text" title="删除所有映射" @click="removeSource(source)">
                <v-icon size="small" color="error">mdi-delete</v-icon>
              </v-btn>
            </template>
          </v-list-item>
        </v-list>
      </div>

      <!-- 添加新映射 -->
      <div class="d-flex align-center ga-2 mb-3">
        <v-btn size="small" color="primary" variant="tonal" @click="localSource = '*'">
          使用 * 匹配任意源模型
        </v-btn>
        <span class="text-caption text-medium-emphasis">选择后，任何请求模型都会映射到右侧目标模型。</span>
      </div>
      <div class="d-flex align-center ga-2">
        <v-combobox
          v-model="localSource"
          label="源模型名"
          :items="sourceOptions"
          item-title="title"
          item-value="value"
          variant="outlined"
          density="comfortable"
          hide-details
          class="flex-1-1"
          placeholder="选择或输入源模型名"
          clearable
          @keyup.enter="addMapping"
        />
        <v-icon color="primary">mdi-arrow-right</v-icon>
        <v-combobox
          v-model="localTarget"
          label="目标模型名"
          :placeholder="targetPlaceholder"
          :items="availableTargetOptions"
          :loading="fetchingModels"
          variant="outlined"
          density="comfortable"
          hide-details
          class="flex-1-1"
          clearable
          @focus="fetchModels"
          @keyup.enter="addMapping"
        />
        <v-btn
          color="secondary"
          variant="elevated"
          :disabled="!isValid"
          @click="addMapping"
        >
          添加
        </v-btn>
      </div>
      <div v-if="fetchError" class="text-error text-caption mt-2">
        {{ fetchError }}
      </div>
      <div v-else-if="localSource && existingTargets.length" class="text-caption text-medium-emphasis mt-2">
        当前源模型已配置 {{ existingTargets.length }} 个目标，可继续追加。
      </div>
    </v-card-text>
  </v-card>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import draggable from 'vuedraggable'

const props = defineProps<{
  modelValue: Record<string, string[]>
  hint: string
  sourceOptions: Array<{ title: string; value: string }>
  targetOptions: Array<{ title: string; value: string }>
  targetPlaceholder: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, string[]>]
  'fetch-models': [source: string]
}>()

const localSource = ref('')
const localTarget = ref('')
const fetchingModels = ref(false)
const fetchError = ref('')

const existingTargets = computed(() => {
  const src = getStringValue(localSource.value)
  if (!src) return []
  return props.modelValue[src] || []
})

const availableTargetOptions = computed(() => {
  const existing = new Set(existingTargets.value)
  return props.targetOptions.filter(opt => !existing.has(opt.value))
})

const isValid = computed(() => {
  const src = getStringValue(localSource.value)
  const tgt = getStringValue(localTarget.value)
  if (!src || !tgt) return false
  return !existingTargets.value.includes(tgt)
})

const getStringValue = (val: unknown): string => {
  if (!val) return ''
  if (typeof val === 'string') return val
  if (typeof val === 'object' && 'value' in val) {
    const value = (val as { value?: unknown }).value
    return typeof value === 'string' ? value : ''
  }
  return ''
}

const getTargetKey = (target: string): string => target

const fetchModels = () => {
  const src = getStringValue(localSource.value)
  if (src) {
    emit('fetch-models', src)
  }
}

const addMapping = () => {
  const src = getStringValue(localSource.value)
  const tgt = getStringValue(localTarget.value)
  if (!src || !tgt) return
  if (existingTargets.value.includes(tgt)) return

  const updated = { ...props.modelValue }
  if (!updated[src]) {
    updated[src] = []
  }
  updated[src] = [...updated[src], tgt]
  emit('update:modelValue', updated)
  localTarget.value = ''
}

const removeTarget = (source: string, target: string) => {
  const updated = { ...props.modelValue }
  if (!updated[source]) return
  updated[source] = updated[source].filter(t => t !== target)
  if (updated[source].length === 0) {
    delete updated[source]
  }
  emit('update:modelValue', updated)
}

const reorderTargets = (source: string, targets: string[]) => {
  const updated = { ...props.modelValue, [source]: [...targets] }
  emit('update:modelValue', updated)
}

const removeSource = (source: string) => {
  const updated = { ...props.modelValue }
  delete updated[source]
  emit('update:modelValue', updated)
}

// Expose fetch state for parent control
defineExpose({ fetchingModels, fetchError })
</script>

<style scoped>
.mapping-target-drag-handle {
  cursor: grab;
}

.mapping-target-drag-handle:active {
  cursor: grabbing;
}

.mapping-target-ghost {
  opacity: 0.45;
}
</style>
