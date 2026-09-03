<template>
  <div class="json-body-pane">
    <div v-if="isLarge" class="json-body-toolbar">
      <span>{{ expanded ? `已展开 ${formattedSize}` : `正文约 ${formattedSize}，展开后再渲染` }}</span>
      <v-btn size="small" variant="tonal" :prepend-icon="expanded ? 'mdi-chevron-up' : 'mdi-chevron-down'" @click="expanded = !expanded">
        {{ expanded ? '收起' : '展开' }}
      </v-btn>
    </div>
    <pre v-if="!isLarge || expanded" class="json-body" :class="{ wrap }">{{ formattedBody }}</pre>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const LARGE_BODY_CHARS = 200 * 1024

const props = defineProps<{
  body?: string
  wrap?: boolean
}>()

const expanded = ref(false)

watch(
  () => props.body,
  () => {
    expanded.value = false
  }
)

const rawLength = computed(() => props.body?.length || 0)
const isLarge = computed(() => rawLength.value > LARGE_BODY_CHARS)

const formattedSize = computed(() => {
  const bytes = rawLength.value
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
})

const formattedBody = computed(() => {
  if (isLarge.value && !expanded.value) return ''
  return formatJsonOrText(props.body || '')
})

const formatJsonOrText = (raw: string) => {
  const trimmed = raw.trim()
  if (!trimmed) return ''
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return raw
  }
}
</script>

<style scoped>
.json-body-pane {
  min-height: 0;
}

.json-body-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px;
  font-size: 13px;
  color: rgb(var(--v-theme-on-surface));
  position: sticky;
  top: 0;
  z-index: 1;
  background: rgb(var(--v-theme-surface));
  border-bottom: 1px solid rgba(var(--v-theme-on-surface), 0.12);
}

.json-body {
  margin: 0;
  padding: 12px 14px;
  max-height: 40vh;
  overflow: auto;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre;
  word-break: break-word;
  background: rgba(var(--v-theme-on-surface), 0.04);
}

.json-body.wrap {
  white-space: pre-wrap;
}
</style>
