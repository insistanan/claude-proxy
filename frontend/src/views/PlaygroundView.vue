<template>
  <v-container fluid class="playground-container pa-4">
    <v-card class="playground-card" elevation="2" rounded="lg">
      <!-- 顶部选择区 -->
      <v-card-title class="d-flex align-center ga-4 pa-4 bg-surface">
        <v-icon size="28">mdi-test-tube</v-icon>
        <span class="text-h5 font-weight-bold">API 演练台</span>
      </v-card-title>

      <v-divider />

      <v-card-text class="pa-4">
        <!-- 协议、渠道和模型选择 -->
        <v-row class="mb-4">
          <v-col cols="12" md="4">
            <v-select
              v-model="playgroundStore.apiType"
              :items="apiTypeOptions"
              label="选择 API 协议"
              variant="outlined"
              prepend-inner-icon="mdi-api"
              @update:model-value="onApiTypeChange"
            />
          </v-col>
          <v-col cols="12" md="4">
            <v-select
              v-model="playgroundStore.channelIndex"
              :items="channelOptions"
              :disabled="!playgroundStore.apiType"
              label="选择渠道"
              variant="outlined"
              prepend-inner-icon="mdi-server-network"
              @update:model-value="onChannelChange"
            />
          </v-col>
          <v-col cols="12" md="4">
            <v-select
              v-model="selectedModel"
              :items="modelOptions"
              :loading="isLoadingModels"
              :disabled="!playgroundStore.channelIndex"
              label="选择模型"
              variant="outlined"
              prepend-inner-icon="mdi-robot"
            >
              <template #no-data>
                <div class="text-center pa-4 text-medium-emphasis">
                  {{ modelsError || '请先选择渠道' }}
                </div>
              </template>
            </v-select>
          </v-col>
        </v-row>

        <!-- 对话区域 -->
        <v-card variant="outlined" class="message-area mb-4" rounded="lg">
          <v-card-text class="pa-4" style="height: 500px; overflow-y: auto;" ref="messageContainer">
            <div v-if="playgroundStore.messages.length === 0" class="text-center text-medium-emphasis py-8">
              <v-icon size="64" color="grey-lighten-1">mdi-chat-outline</v-icon>
              <div class="text-h6 mt-4">选择协议和渠道开始对话</div>
            </div>
            <div v-else class="messages-list">
              <div
                v-for="msg in playgroundStore.messages"
                :key="msg.id"
                class="message-bubble mb-3"
                :class="msg.role === 'user' ? 'user-message' : 'assistant-message'"
              >
                <div class="message-header d-flex align-center ga-2 mb-1">
                  <v-icon size="16">{{ msg.role === 'user' ? 'mdi-account' : 'mdi-robot' }}</v-icon>
                  <span class="text-caption font-weight-bold">{{ msg.role === 'user' ? '用户' : 'AI' }}</span>
                  <span class="text-caption text-medium-emphasis">{{ formatTime(msg.timestamp) }}</span>
                </div>
                <div class="message-content">{{ msg.content }}</div>
              </div>
            </div>
          </v-card-text>
        </v-card>

        <!-- 输入区域 -->
        <v-row>
          <v-col cols="12">
            <v-textarea
              v-model="userInput"
              label="输入消息..."
              variant="outlined"
              rows="3"
              :disabled="!canInput"
              :loading="playgroundStore.isStreaming"
              @keydown.ctrl.enter="sendMessage"
            />
          </v-col>
        </v-row>

        <!-- 操作按钮 -->
        <v-row>
          <v-col cols="12" class="d-flex ga-2 justify-end">
            <v-btn
              color="error"
              variant="text"
              prepend-icon="mdi-delete"
              @click="clearMessages"
              :disabled="playgroundStore.messages.length === 0"
            >
              清空对话
            </v-btn>
            <v-btn
              color="primary"
              prepend-icon="mdi-send"
              :disabled="!canSend"
              :loading="playgroundStore.isStreaming"
              @click="sendMessage"
            >
              发送 (Ctrl+Enter)
            </v-btn>
          </v-col>
        </v-row>
      </v-card-text>
    </v-card>

    <!-- 错误提示 -->
    <v-snackbar v-model="showError" color="error" :timeout="3000">
      {{ errorMessage }}
    </v-snackbar>
  </v-container>
</template>

<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue'
import { usePlaygroundStore } from '@/stores/playground'
import { useChannelStore } from '@/stores/channel'
import { testChannelWithModel, fetchUpstreamModels } from '@/services/api'

const playgroundStore = usePlaygroundStore()
const channelStore = useChannelStore()

const userInput = ref('')
const showError = ref(false)
const errorMessage = ref('')
const messageContainer = ref<HTMLElement>()

// 模型选择相关
const isLoadingModels = ref(false)
const modelsError = ref<string | null>(null)
const modelOptions = ref<Array<{ title: string; value: string }>>([])
const selectedModel = ref<string | null>(null)

const apiTypeOptions = [
  { title: 'Messages', value: 'messages' },
  { title: 'Responses', value: 'responses' },
  { title: 'Google Gemini', value: 'gemini' },
  { title: 'OpenAI Chat', value: 'chat' },
  { title: 'Images', value: 'images' }
]

const channelOptions = computed(() => {
  if (!playgroundStore.apiType) return []
  
  const channels = channelStore.getChannelsByType(playgroundStore.apiType)
  return channels.map(ch => ({
    title: `#${ch.index} - ${ch.name}`,
    value: ch.index
  }))
})

const canSend = computed(() => {
  return !!(
    playgroundStore.apiType &&
    playgroundStore.channelIndex !== null &&
    selectedModel.value &&
    userInput.value.trim() &&
    !playgroundStore.isStreaming
  )
})

const canInput = computed(() => {
  return !!(
    playgroundStore.apiType &&
    playgroundStore.channelIndex !== null &&
    !playgroundStore.isStreaming
  )
})

const onApiTypeChange = (value: 'messages' | 'responses' | 'gemini' | 'chat' | 'images' | null) => {
  playgroundStore.setApiType(value)
  selectedModel.value = null
  modelOptions.value = []
}

const onChannelChange = (value: number | null) => {
  playgroundStore.setChannel(value)
  if (value !== null) {
    loadModels()
  } else {
    selectedModel.value = null
    modelOptions.value = []
  }
}

const loadModels = async () => {
  const channel = channelStore.getChannelsByType(playgroundStore.apiType!)
    .find(ch => ch.index === playgroundStore.channelIndex)
  
  if (!channel || !channel.apiKeys.length) {
    modelsError.value = '渠道未配置 API Key'
    return
  }

  isLoadingModels.value = true
  modelsError.value = null
  modelOptions.value = []
  selectedModel.value = null

  try {
    const result = await fetchUpstreamModels(
      channel.baseUrl,
      channel.apiKeys[0]
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

const formatTime = (timestamp: number) => {
  const date = new Date(timestamp)
  return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

const scrollToBottom = async () => {
  await nextTick()
  if (messageContainer.value) {
    messageContainer.value.scrollTop = messageContainer.value.scrollHeight
  }
}

const sendMessage = async () => {
  if (!canSend.value) return

  const message = userInput.value.trim()
  userInput.value = ''

  playgroundStore.addMessage({
    role: 'user',
    content: message
  })

  scrollToBottom()

  try {
    playgroundStore.setStreaming(true)
    playgroundStore.addMessage({
      role: 'assistant',
      content: ''
    })

    await testChannelWithModel(
      playgroundStore.apiType!,
      playgroundStore.channelIndex!,
      selectedModel.value!,
      message,
      (chunk: string) => {
        playgroundStore.updateLastMessage(
          playgroundStore.messages[playgroundStore.messages.length - 1].content + chunk
        )
        scrollToBottom()
      }
    )
  } catch (error: any) {
    errorMessage.value = error.message || '发送失败'
    showError.value = true
    playgroundStore.messages.pop()
  } finally {
    playgroundStore.setStreaming(false)
    scrollToBottom()
  }
}

const clearMessages = () => {
  playgroundStore.clearMessages()
}

watch(() => playgroundStore.messages.length, () => {
  scrollToBottom()
})
</script>

<style scoped>
.playground-container {
  max-width: 1200px;
  margin: 0 auto;
}

.playground-card {
  min-height: 600px;
}

.message-area {
  background: rgba(var(--v-theme-surface), 0.3);
}

.messages-list {
  display: flex;
  flex-direction: column;
}

.message-bubble {
  max-width: 80%;
  padding: 12px;
  border-radius: 12px;
}

.user-message {
  align-self: flex-end;
  background: rgb(var(--v-theme-primary));
  color: white;
  margin-left: auto;
}

.assistant-message {
  align-self: flex-start;
  background: rgb(var(--v-theme-surface-light));
  border: 1px solid rgba(var(--v-border-color), 0.2);
}

.message-content {
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
