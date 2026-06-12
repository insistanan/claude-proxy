import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
  timestamp: number
}

export interface PlaygroundState {
  apiType: 'messages' | 'responses' | 'gemini' | 'chat' | null
  channelIndex: number | null
  messages: Message[]
  isStreaming: boolean
  sessionId: string | null
  threadId: string | null
  interactionId: string | null
}

// 生成 UUID v4
function generateUUID(): string {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = Math.random() * 16 | 0
    const v = c === 'x' ? r : (r & 0x3 | 0x8)
    return v.toString(16)
  })
}

export const usePlaygroundStore = defineStore('playground', () => {
  const apiType = ref<PlaygroundState['apiType']>(null)
  const channelIndex = ref<PlaygroundState['channelIndex']>(null)
  const messages = ref<Message[]>([])
  const isStreaming = ref(false)
  
  // 会话标识符（会话期间保持不变）
  const sessionId = ref<string | null>(null)
  const threadId = ref<string | null>(null)
  const interactionId = ref<string | null>(null)

  const setApiType = (type: PlaygroundState['apiType']) => {
    apiType.value = type
    channelIndex.value = null
    messages.value = []
    // 重置会话 ID
    sessionId.value = null
    threadId.value = null
    interactionId.value = null
  }

  const setChannel = (index: number) => {
    channelIndex.value = index
    messages.value = []
    // 初始化会话 ID
    if (!sessionId.value) {
      sessionId.value = generateUUID()
      threadId.value = `thread-${generateUUID()}`
    }
  }

  const addMessage = (message: Omit<Message, 'id' | 'timestamp'>) => {
    messages.value.push({
      ...message,
      id: Date.now().toString() + Math.random(),
      timestamp: Date.now()
    })
  }

  const updateLastMessage = (content: string) => {
    if (messages.value.length > 0) {
      const last = messages.value[messages.value.length - 1]
      last.content = content
    }
  }

  const clearMessages = () => {
    messages.value = []
    // 清空对话时重置会话 ID
    sessionId.value = null
    threadId.value = null
    interactionId.value = null
  }

  const setStreaming = (streaming: boolean) => {
    isStreaming.value = streaming
  }

  const setInteractionId = (id: string) => {
    interactionId.value = id
  }

  return {
    apiType,
    channelIndex,
    messages,
    isStreaming,
    sessionId,
    threadId,
    interactionId,
    setApiType,
    setChannel,
    addMessage,
    updateLastMessage,
    clearMessages,
    setStreaming,
    setInteractionId
  }
})
