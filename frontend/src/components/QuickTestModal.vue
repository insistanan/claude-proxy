<template>
  <v-dialog v-model="isOpen" max-width="800" persistent>
    <v-card rounded="lg">
      <v-card-title class="d-flex align-center justify-space-between pa-4">
        <div class="d-flex align-center ga-2">
          <v-icon color="success">mdi-test-tube</v-icon>
          <span class="text-h6">快捷测试</span>
          <v-chip v-if="channel" size="small" color="primary" variant="tonal">
            {{ channel.name }}
          </v-chip>
        </div>
        <v-btn icon size="small" variant="text" @click="close">
          <v-icon>mdi-close</v-icon>
        </v-btn>
      </v-card-title>

      <v-divider />

      <v-card-text class="pa-4">
        <!-- 模型选择 -->
        <v-select
          v-model="selectedModel"
          :items="modelOptions"
          :loading="isLoadingModels"
          label="选择模型"
          variant="outlined"
          prepend-inner-icon="mdi-robot"
          density="comfortable"
          class="mb-4"
          :disabled="isSending"
        >
          <template #no-data>
            <div class="text-center pa-4 text-medium-emphasis">
              {{ modelsError || '无可用模型' }}
            </div>
          </template>
        </v-select>

        <!-- 测试输入 -->
        <v-textarea
          v-model="testMessage"
          label="测试消息"
          variant="outlined"
          rows="3"
          :disabled="isSending"
          :loading="isSending"
          placeholder="输入测试消息..."
          class="mb-4"
        />

        <!-- 发送按钮 -->
        <div class="d-flex justify-end mb-4">
          <v-btn
            color="success"
            prepend-icon="mdi-send"
            :disabled="!canSend"
            :loading="isSending"
            @click="sendTest"
          >
            发送测试
          </v-btn>
        </div>

        <!-- 响应结果 -->
        <v-card v-if="responseText || isSending" variant="outlined" class="response-area">
          <v-card-title class="text-subtitle-2 pa-3 bg-surface">
            <v-icon size="small" class="mr-2">mdi-message-reply-text</v-icon>
            响应结果
          </v-card-title>
          <v-divider />
          <v-card-text class="pa-4" style="max-height: 300px; overflow-y: auto;">
            <div v-if="responseError" class="error-message">
              <v-icon color="error" class="mr-2">mdi-alert-circle</v-icon>
              <span class="text-error">{{ responseError }}</span>
            </div>
            <div v-else-if="isSending && !responseText" class="text-center text-medium-emphasis py-4">
              <v-progress-circular indeterminate size="32" />
              <div class="mt-2">正在发送请求...</div>
            </div>
            <div v-else class="response-text">{{ responseText }}</div>
          </v-card-text>
        </v-card>
      </v-card-text>
    </v-card>
  </v-dialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import type { Channel } from '@/services/api'
import { fetchUpstreamModels, testChannelWithModel } from '@/services/api'

const props = defineProps<{
  modelValue: boolean
  channel: Channel | null
  apiType: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
}>()

const isOpen = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value)
})

const isLoadingModels = ref(false)
const modelsError = ref<string | null>(null)
const modelOptions = ref<Array<{ title: string; value: string }>>([])
const selectedModel = ref<string | null>(null)
const testMessage = ref('你好')
const isSending = ref(false)
const responseText = ref('')
const responseError = ref<string | null>(null)

const canSend = computed(() => {
  return !!(selectedModel.value && testMessage.value.trim() && !isSending.value)
})

const close = () => {
  isOpen.value = false
}

const loadModels = async () => {
  if (!props.channel || !props.channel.apiKeys.length) {
    modelsError.value = '渠道未配置 API Key'
    return
  }

  isLoadingModels.value = true
  modelsError.value = null
  modelOptions.value = []
  selectedModel.value = null

  try {
    const result = await fetchUpstreamModels(
      props.channel.baseUrl,
      props.channel.apiKeys[0],
      props.channel.serviceType,
      {
        baseUrls: props.channel.baseUrls,
        insecureSkipVerify: props.channel.insecureSkipVerify,
        proxyMode: props.channel.proxyMode,
        proxyUrl: props.channel.proxyUrl
      }
    )
    
    if (result.data && result.data.length > 0) {
      modelOptions.value = result.data.map(model => ({
        title: model.id,
        value: model.id
      }))
      // 自动选择第一个模型
      selectedModel.value = result.data[0].id
    } else {
      modelsError.value = '未找到可用模型'
    }
  } catch (error: any) {
    console.error('加载模型失败:', error)
    modelsError.value = error.message || '加载模型失败'
  } finally {
    isLoadingModels.value = false
  }
}

const sendTest = async () => {
  if (!canSend.value || !props.channel) return

  isSending.value = true
  responseText.value = ''
  responseError.value = null

  try {
    await testChannelWithModel(
      props.apiType,
      props.channel.index,
      selectedModel.value!,
      testMessage.value.trim(),
      (chunk: string) => {
        responseText.value += chunk
      }
    )
  } catch (error: any) {
    console.error('测试失败:', error)
    responseError.value = error.message || '测试失败'
  } finally {
    isSending.value = false
  }
}

// 监听弹窗打开，自动加载模型
watch(() => props.modelValue, (newVal) => {
  if (newVal && props.channel) {
    // 重置状态
    responseText.value = ''
    responseError.value = null
    testMessage.value = '你好'
    loadModels()
  }
})
</script>

<style scoped>
.response-area {
  border: 1px solid rgba(var(--v-border-color), 0.2);
}

.response-text {
  white-space: pre-wrap;
  word-break: break-word;
  font-family: 'Consolas', 'Monaco', monospace;
  font-size: 14px;
  line-height: 1.6;
}

.error-message {
  display: flex;
  align-items: center;
  color: rgb(var(--v-theme-error));
  padding: 12px;
  background: rgba(var(--v-theme-error), 0.1);
  border-radius: 8px;
}
</style>
