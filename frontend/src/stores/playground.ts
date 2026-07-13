import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
  timestamp: number
}

export interface PlaygroundState {
  apiType: 'messages' | 'responses' | 'gemini' | 'chat' | 'images' | null
  channelIndex: number | null
  messages: Message[]
  isStreaming: boolean
  sessionId: string | null
  threadId: string | null
  interactionId: string | null
  responseId: string | null
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
  const responseId = ref<string | null>(null)

  const resetConversationContext = () => {
    sessionId.value = null
    threadId.value = null
    interactionId.value = null
    responseId.value = null
  }

  const ensureConversationContext = () => {
    if (!sessionId.value) {
      sessionId.value = generateUUID()
    }
    if (!threadId.value) {
      threadId.value = `thread-${generateUUID()}`
    }
  }

  const setApiType = (type: PlaygroundState['apiType']) => {
    apiType.value = type
    channelIndex.value = null
    messages.value = []
    resetConversationContext()
  }

  const setChannel = (index: PlaygroundState['channelIndex']) => {
    channelIndex.value = index
    messages.value = []
    resetConversationContext()
    if (index !== null) {
      ensureConversationContext()
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
    resetConversationContext()
    if (channelIndex.value !== null) {
      ensureConversationContext()
    }
  }

  const setStreaming = (streaming: boolean) => {
    isStreaming.value = streaming
  }

  const setInteractionId = (id: string) => {
    interactionId.value = id
  }

  const setResponseId = (id: string | null) => {
    responseId.value = id
  }

  return {
    apiType,
    channelIndex,
    messages,
    isStreaming,
    sessionId,
    threadId,
    interactionId,
    responseId,
    setApiType,
    setChannel,
    addMessage,
    updateLastMessage,
    clearMessages,
    setStreaming,
    setInteractionId,
    setResponseId
  }
})
