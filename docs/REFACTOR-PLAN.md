# 架构收敛与简化执行方案

> 本文是一份**可机械执行**的重构工单。每一批（P0~P3）都独立可交付、可回滚。
> 执行者请严格按顺序做，**不要跳批**，不要在一批未验证通过前开始下一批。

## 0. 执行纪律（必读）

1. **不改变对外行为**。本轮全部是结构收敛，HTTP 响应体、字段名、错误码、日志文案一律保持不变。唯一允许的行为变化在 P1 中已显式列出（渠道 DTO 字段补齐），且是修 bug。
2. **每完成一个小节就验证一次**，不要攒到最后：
   - 后端：`cd backend-go && go build ./... && go vet ./...`
   - 后端测试：`cd backend-go && go test ./...`
   - 前端：`cd frontend && bun run build`（无 bun 用 `npm run build`）
3. **禁止顺手改逻辑**。发现可疑代码 → 记进"遗留问题"清单，不要就地修。
4. **禁止 git commit / push**，除非用户明确要求。
5. 每批完成后在本文件对应小节勾选 `[x]`，并把 `go build` / 前端 build 的结果贴进"验证记录"。

---

## 1. P0 · 清理死代码

### 1.1 已完成部分（勿重复执行）

以下改动**已经落盘**，执行者请先 `git diff` 确认存在，不要重做：

**前端删除文件**
- [x] 删除 `frontend/src/components/ChannelCard.vue`（1196 行，全仓库零引用）
- [x] 删除 `frontend/src/components/ChannelMetricsChart.vue`（313 行，零引用）

**后端 `internal/handlers/{messages,responses,gemini,chat,images}/channels.go`**
- [x] 五个文件各删除 13 个零调用的 `gin.HandlerFunc` 包装（`GetUpstreams` / `AddUpstream` / `UpdateUpstream` / `DeleteUpstream` / `AddApiKey` / `DeleteApiKey` / `MoveApiKeyToTop` / `MoveApiKeyToBottom` / `ReorderChannels` / `SetChannelStatus` / `SetChannelPromotion` / `PingChannel` / `PingAllChannels`）
- [x] 每个文件只保留 `Crud()` 和 `UpdateLoadBalance()` 两个导出函数（后者被 `main.go` 的 `RegisterChannelRoutes` 使用，**必须保留**）

**后端删除的零调用导出函数**
- [x] `internal/utils/headers.go`：`PrepareMinimalHeaders`、`EnsureCodexHeaders`、`EnsureClaudeCodeHeaders`
- [x] `internal/utils/json.go`：`SimplifyToolsInJSON`
- [x] `internal/converters/responses_converter.go`：`ResponsesToClaudeMessages`（薄包装，真实链路走 `ResponsesToClaudeMessagesWithOptions`）
- [x] `internal/metrics/channel_metrics.go`：`GenerateMetricsKey`
- [x] `internal/session/manager.go`：`NewSessionManager`（纯内存版构造器，主链路只用 `NewPersistentSessionManager`）
- [x] `internal/converters/factory.go`：`NewConverter`（宽松版工厂，仅测试在用；生产走 `NewConverterStrict`）

**后端"导出但仅包内使用"→ 降级为非导出**
- [x] `converters`：`JSONMarshal`→`jsonMarshal`、`JSONUnmarshal`→`jsonUnmarshal`、`ResponsesToolsToGeminiTools`→`responsesToolsToGeminiTools`、`ResponsesToolChoiceToGemini`→`responsesToolChoiceToGemini`
- [x] `config`：`RedirectModel`→`redirectModel`、`RedirectModelList`→`redirectModelList`
- [x] `utils`：`EstimateMessagesTokens`→`estimateMessagesTokens`
- [x] `modelcatalog`：`LooksLikeChatRouteAlias`→`looksLikeChatRouteAlias`
- [x] `session`：`NewBaseURLAffinityManagerWithTTL`→`newBaseURLAffinityManagerWithTTL`

**后端重复构造器合并**
- [x] `internal/session/trace_affinity.go`：`NewTraceAffinityManager()` 与 `NewTraceAffinityManagerWithTTL()` 原本是两份复制粘贴的实现。已改为 `NewTraceAffinityManager()` 委托 `newTraceAffinityManagerWithTTL(0)`，后者 `ttl <= 0` 时取默认 30 分钟。

**测试同步修改**
- [x] `internal/converters/converter_test.go` 的 `TestConverterFactory`：原本测 `NewConverter("unknown")` 静默回退到 OpenAI 转换器——这是生产链路不存在的行为。已改为测 `NewConverterStrict`，并新增 `gemini` / `unknown` 必须报错的用例。

**前端 `_` 前缀僵尸函数删除**
- [x] `App.vue`：`_handleError`、`_openAddKeyModal`、`_removeApiKey`、`_updateLoadBalance`
- [x] `components/AddChannelModal.vue`：`_getDefaultBaseUrl`、`_expectedRequestUrl`（后者是 `getExpectedRequestUrl(url)` 的无参重复实现）
- [x] `components/ChannelOrchestration.vue`：`_getActivityAreaPath`、`_getActivityGradient`
- [x] `composables/useTheme.ts`：`_vuetifyTheme` 及随之无用的 `import { useTheme as useVuetifyTheme } from 'vuetify'`

> 注意：`ChannelOrchestration.vue` 中形如 `const _ = activityUpdateTick.value` 的三处**不是死代码**，是刻意制造的响应式依赖，**必须保留**。

### 1.2 P0 剩余待办

#### [x] 任务 P0-A：清理"添加密钥"孤儿弹窗

**背景**：删除 `App.vue` 的 `_openAddKeyModal` 后暴露出一条完全没有入口的 UI 链路——`dialogStore.openAddKeyModal()` 现在零调用，意味着 `App.vue` 里那个 `v-dialog` 永远打不开。这条链路在删除前就已经是死的（`_openAddKeyModal` 本身就没有调用者）。

**先做确认**（不要跳过）：
```bash
cd frontend/src
grep -rn "openAddKeyModal" --include=*.vue --include=*.ts .
```
若结果**只有** `stores/dialog.ts` 里的定义和 return，则确认为死链路，按下面执行；若发现别处有调用，**停止并上报**。

**改动清单**：
1. `frontend/src/App.vue`
   - 删除 template 中 `v-model="dialogStore.showAddKeyModal"` 的整个 `<v-dialog>` 块（含标题、`v-text-field`、取消/添加按钮）
   - 删除 script 中的 `addApiKey` 函数
2. `frontend/src/stores/dialog.ts`
   - 删除 state：`showAddKeyModal`、`selectedChannelForKey`、`newApiKey`
   - 删除方法：`openAddKeyModal`、`closeAddKeyModal`
   - 从 `return {}` 中移除以上全部
3. 删除后再次全量 grep 上述 5 个符号，确认零残留

> `components/AddChannelModal.vue` 里也有同名的 `newApiKey` / `addApiKey`，那是**组件自己的局部实现，与 dialog store 无关，不要动**。

#### [x] 任务 P0-B：编译与测试验证

```bash
cd backend-go && go build ./... && go vet ./... && go test ./...
cd ../frontend && bun run build
```
全部通过后，在下方"验证记录"填写结果。若 `go test` 有失败用例，先判断是否本轮改动引起（重点看 `converters` 包），是则修，否则记入"遗留问题"。

---

## 2. P1 · 统一渠道序列化（同时修一个真实 bug）

### 2.1 问题陈述

同一个 `config.UpstreamConfig → JSON` 的映射表被**手写了 5 份**，且字段集已经漂移：

| 实现位置 | `proxyMode`/`proxyUrl` | `defaultModel` | `promotionCount` | gemini 的 thoughtSignature 两字段 |
|---|:-:|:-:|:-:|:-:|
| `core/channelcrud/channelcrud.go` → `GetUpstreams` | ✅ | ❌ | ✅ | ❌ |
| `handlers/channel_metrics_handler.go` → `GetChannelDashboard` | ❌ | ✅ | ✅ | ❌ |
| `handlers/gemini/dashboard.go` → `GetDashboard` | ❌ | ❌ | ❌ | ✅ |
| `handlers/chat/dashboard.go` → `GetDashboard` | ❌ | ✅ | ✅ | ❌ |
| `handlers/images/dashboard.go` → `GetDashboard` | ❌ | ✅ | ✅ | ❌ |

**后果**：前端调 `GET /api/{kind}/channels` 和 `GET /api/{kind}/channels/dashboard` 拿到的渠道对象字段不一样，加字段要改 5 处，漏一处就是偶发的"某页面显示不出来"。

**本批唯一允许的行为变化**：统一后所有入口都返回**字段并集**。这是修 bug，不是回归。

### 2.2 [x] 任务 P1-A：新建单一真相源

新建文件 `backend-go/internal/config/channel_dto.go`：

```go
package config

// ChannelToDTO 是渠道对象序列化到管理 API 的**唯一**真相源。
//
// 收敛前：channelcrud.GetUpstreams、handlers.GetChannelDashboard、
// gemini/chat/images 三份 dashboard 各手写一份字段表，字段集已经漂移
// （proxyMode/defaultModel/promotionCount/thoughtSignature 各缺各的），
// 导致同一个渠道从不同端点取出来长得不一样。
//
// 新增渠道字段时**只改这里**。
func ChannelToDTO(up *UpstreamConfig, index int) map[string]interface{} {
	return map[string]interface{}{
		"id":                          up.ID,
		"poolId":                      up.PoolID,
		"index":                       index,
		"name":                        up.Name,
		"serviceType":                 up.ServiceType,
		"baseUrl":                     up.BaseURL,
		"baseUrls":                    up.BaseURLs,
		"apiKeys":                     up.APIKeys,
		"description":                 up.Description,
		"website":                     up.Website,
		"insecureSkipVerify":          up.InsecureSkipVerify,
		"proxyMode":                   up.ProxyMode,
		"proxyUrl":                    up.ProxyURL,
		"modelMapping":                up.ModelMapping,
		"defaultModel":                up.DefaultModel,
		"latency":                     nil,
		"status":                      GetChannelStatus(up),
		"priority":                    GetChannelPriority(up, index),
		"promotionUntil":              up.PromotionUntil,
		"promotionCount":              up.PromotionCount,
		"lowQuality":                  up.LowQuality,
		"visionCapable":               up.VisionCapable,
		"excludeFromConversation":     up.ExcludeFromConversation,
		"disablePromptCacheKey":       up.DisablePromptCacheKey,
		"visionLayerEnabled":          up.VisionLayerEnabled,
		"visionLayerChannelId":        up.VisionLayerChannelID,
		"visionLayerModel":            up.VisionLayerModel,
		"injectDummyThoughtSignature": up.InjectDummyThoughtSignature,
		"stripThoughtSignature":       up.StripThoughtSignature,
	}
}

// ChannelListToDTO 批量序列化，自动跳过 status == deleted 的渠道。
// 返回的 index 是**原切片下标**（前端和调度器都按它定位渠道，不能压缩）。
func ChannelListToDTO(list []UpstreamConfig) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for i := range list {
		if GetChannelStatus(&list[i]) == ChannelStatusDeleted {
			continue
		}
		out = append(out, ChannelToDTO(&list[i], i))
	}
	return out
}
```

**关键约束**（做错会引入 bug）：
- `index` 必须是**原切片下标 `i`**，不是过滤后的序号。渠道的所有操作（reorder / status / metrics）都按这个 index 定位。
- 循环里用 `&list[i]` 取指针，**不要** `for _, up := range list` 再 `&up`（旧代码就是这么写的，虽然当前没触发 bug，但每次迭代 `up` 是副本）。
- 若 `UpstreamConfig` 上没有 `ProxyMode` / `ProxyURL` / `InjectDummyThoughtSignature` / `StripThoughtSignature` 字段，去 `internal/config/config.go` 确认准确的字段名后再填，**不要臆造**。

### 2.3 [x] 任务 P1-B：改造 5 处调用点

`gin.H` 就是 `map[string]interface{}`，可直接赋值，无需转换。

1. **`internal/core/channelcrud/channelcrud.go`** → `Handlers.GetUpstreams`
   把手写的 `for` + `gin.H{...}` 整段替换为：
   ```go
   func (h *Handlers) GetUpstreams(c *gin.Context) {
       c.JSON(200, gin.H{
           "channels":    config.ChannelListToDTO(h.ops.List()),
           "loadBalance": h.ops.LoadBalance(),
       })
   }
   ```

2. **`internal/handlers/channel_metrics_handler.go`** → `GetChannelDashboard`
   把构建 `channels` 的那段 `for` 循环替换为 `channels := config.ChannelListToDTO(upstreams)`。
   **其余部分（metrics / stats / recentActivity 的组装）一律不动。**

3~5. **删除** `internal/handlers/gemini/dashboard.go`、`internal/handlers/chat/dashboard.go`、`internal/handlers/images/dashboard.go` 三个文件。
   然后修改 `backend-go/main.go`，把这三个 kind 的 `Dashboard` 选项换成通用版：
   ```go
   // 原来：Dashboard: gemini.GetDashboard(cfgManager, channelScheduler),
   Dashboard: handlers.GetChannelDashboard(cfgManager, channelScheduler, scheduler.ChannelKindGemini),
   // chat / images 同理，只改 kind 参数
   ```

   > 已核对：chat 与 images 两份 dashboard 与通用版**逻辑逐字相同**，仅 kind 硬编码不同，可安全删除。gemini 版的差异只有多出的两个 thoughtSignature 字段，已并入 `ChannelToDTO`，同样可安全删除。

   删除后检查这三个包是否还有别的文件在用 `dashboard.go` 里引入的 import，若某个包只剩 `handler.go` + `channels.go`，确认没有编译错误即可。

### 2.4 [x] 任务 P1-C：消除 metrics manager 的 switch

**现状**：`main.go` 已经用 `map[scheduler.ChannelKind]*metrics.MetricsManager` 收敛了初始化，但 `ChannelScheduler` 仍只暴露 5 个具名 getter（`GetMessagesMetricsManager` / `GetResponsesMetricsManager` / `GetGeminiMetricsManager` / `GetChatMetricsManager` / `GetImagesMetricsManager`），导致 `GetChannelDashboard` 里又要写一次 `switch kind` 把 kind 转回 manager。

**改动**：在 `internal/scheduler/channel_scheduler.go` 增加：
```go
// MetricsManager 按渠道类型返回对应的指标管理器。
// 调用方不应再用 GetXxxMetricsManager + switch kind 的写法。
func (s *ChannelScheduler) MetricsManager(kind ChannelKind) *metrics.MetricsManager {
	switch kind {
	case ChannelKindResponses:
		return s.responsesMetrics
	case ChannelKindGemini:
		return s.geminiMetrics
	case ChannelKindChat:
		return s.chatMetrics
	case ChannelKindImages:
		return s.imagesMetrics
	default:
		return s.messagesMetrics
	}
}
```
> 字段名以 `ChannelScheduler` 结构体实际定义为准，先读结构体再写。

然后把 `GetChannelDashboard` 里的 `switch kind` 换成 `metricsManager := sch.MetricsManager(kind)`。

**5 个具名 getter 暂时保留**（其他地方还在用），本批不动它们。

### 2.5 [x] 任务 P1-D：验证

```bash
cd backend-go && go build ./... && go vet ./... && go test ./...
```
另外**人工核对**：启动服务后（或直接读代码核对）确认这两个端点返回的 `channels[]` 元素字段完全一致：
- `GET /api/messages/channels`
- `GET /api/messages/channels/dashboard`

**预期收益**：净减约 400~500 行，字段漂移消失。

---

## 3. P2 · config 层按 ChannelKind 表驱动

### 3.1 问题陈述

`internal/config/` 下五个文件 `config_messages.go` / `config_responses.go` / `config_gemini.go` / `config_chat.go` / `config_images.go`，每个约 190 行，**逐字同构**，仅操作的切片、pools 字段和日志文案不同：

```go
// config_messages.go
func (cm *ConfigManager) AddUpstreamWithResult(u UpstreamConfig) (AddedUpstream, error) { ... cm.config.Upstream ... }
// config_chat.go —— 除了 ChatUpstream/ChatPools 和日志里的 "Chat" 字样，一模一样
func (cm *ConfigManager) AddChatUpstreamWithResult(u UpstreamConfig) (AddedUpstream, error) { ... cm.config.ChatUpstream ... }
```

底层的 `addValidatedUpstreamOp` 等 op 函数**已经**抽出来了，但外层没有按 kind 参数化，所以新增一种渠道类型仍要抄 190 行。

### 3.2 [x] 任务 P2-A：定义 channelSet 抽象

新建 `backend-go/internal/config/channel_set.go`：

```go
package config

// channelSet 把"某一种渠道类型在 Config 里的存放位置"抽象出来，
// 使 CRUD 逻辑不必为每种渠道类型复制一遍。
//
// 注意所有字段都是**指针**：调用方拿到的是 cm.config 里真实切片的地址，
// 赋值 *upstreams = next 才能真正改到配置。
type channelSet struct {
	name        string              // 日志文案，如 "Messages" / "Chat"
	upstreams   *[]UpstreamConfig   // 指向 cm.config 的对应切片
	pools       *[]ChannelPool      // 指向 cm.config 的对应池列表
	loadBalance *string             // 指向 cm.config 的对应负载均衡策略
}

// channelSetFor 返回指定渠道类型的存放位置。
// 调用方**必须已持有 cm.mu**（读或写锁，取决于后续操作）。
func (cm *ConfigManager) channelSetFor(kind string) *channelSet {
	switch kind {
	case "responses":
		return &channelSet{"Responses", &cm.config.ResponsesUpstream, &cm.config.ResponsesPools, &cm.config.ResponsesLoadBalance}
	case "gemini":
		return &channelSet{"Gemini", &cm.config.GeminiUpstream, &cm.config.GeminiPools, &cm.config.GeminiLoadBalance}
	case "chat":
		return &channelSet{"Chat", &cm.config.ChatUpstream, &cm.config.ChatPools, &cm.config.ChatLoadBalance}
	case "images":
		return &channelSet{"Images", &cm.config.ImagesUpstream, &cm.config.ImagesPools, &cm.config.ImagesLoadBalance}
	default:
		return &channelSet{"Messages", &cm.config.Upstream, &cm.config.MessagePools, &cm.config.LoadBalance}
	}
}
```

> `Config` 结构体里 pools 字段的真实名字（`MessagePools` / `ChatPools` / …）以 `internal/config/config.go` 为准，先读再写。

### 3.3 [x] 任务 P2-B：实现通用方法

在同一文件里实现一组以 kind 为参数的通用方法，逐个对照 `config_messages.go` 的现有实现搬迁（**以 messages 版为行为基准**，因为它最完整）：

```go
func (cm *ConfigManager) addChannel(kind string, up UpstreamConfig) (AddedUpstream, error)
func (cm *ConfigManager) updateChannel(kind string, index int, updates UpstreamUpdate) (bool, error)
func (cm *ConfigManager) removeChannel(kind string, index int) (*UpstreamConfig, error)
func (cm *ConfigManager) addChannelKey(kind string, index int, apiKey string) error
func (cm *ConfigManager) removeChannelKey(kind string, index int, apiKey string) error
func (cm *ConfigManager) moveChannelKeyTop(kind string, index int, apiKey string) error
func (cm *ConfigManager) moveChannelKeyBottom(kind string, index int, apiKey string) error
func (cm *ConfigManager) reorderChannels(kind string, order []int) error
func (cm *ConfigManager) setChannelStatusFor(kind string, index int, status string) error
func (cm *ConfigManager) setChannelPromotionFor(kind string, index int, d time.Duration, count int) error
func (cm *ConfigManager) setLoadBalanceFor(kind string, strategy string) error
func (cm *ConfigManager) currentChannelForModel(kind string, model string) (*UpstreamConfig, int, error)
```

**搬迁时必须逐个核对的协议差异**（这些是五份实现里真实存在的分歧，不要一刀切）：
- `config_chat.go` 的 `AddChatUpstreamWithResult` 里有 `upstream.DefaultModel = strings.TrimSpace(...)`，messages 版没有。→ **保留**：在通用版里对所有 kind 都做 TrimSpace（对 messages 无害）。
- 日志文案形如 `[Config-Upstream] 已添加 Chat 上游（优先级1）: %s`，messages 版没有 kind 名。→ 用 `cs.name` 拼接，messages 的 `name` 设为 `"Messages"` 会让日志从"已添加上游"变成"已添加 Messages 上游"。**这属于日志文案变化，可接受，但要在提交说明里写明。**
- 错误文案形如 `无效的 Chat 上游索引: %d`，同上处理。
- 其余任何差异（校验顺序、回滚逻辑、是否触发 `saveConfigLocked`）**必须逐行 diff 五份实现后再决定**，发现无法统一的差异 → 停下上报，不要自行取舍。

**建议的 diff 手法**：
```bash
cd backend-go/internal/config
# 把 chat 版的 kind 特征词抹掉后与 messages 版对比，剩下的就是真实差异
sed 's/Chat//g; s/chat//g' config_chat.go > /tmp/chat.norm
diff /tmp/chat.norm config_messages.go
# gemini / images / responses 同理
```

### 3.4 [x] 任务 P2-C：把旧方法改成薄委托

**不要直接删除**五组旧的导出方法——`channelcrud.Ops` 是用方法值绑定的（`Add: cfgManager.AddChatUpstreamWithResult`），删了要同步改 5 个 `channels.go`。

分两步走，降低一次性风险：

**第一步（本批必做）**：把五个 `config_*.go` 里的方法体全部改成一行委托，文件从 190 行缩到约 60 行：
```go
func (cm *ConfigManager) AddChatUpstreamWithResult(up UpstreamConfig) (AddedUpstream, error) {
	return cm.addChannel("chat", up)
}
```
此时行为不变、调用方不用改，`go test` 应全绿。

**第二步（可选，确认第一步稳定后再做）**：把 5 个 `handlers/*/channels.go` 的 `Ops` 改成闭包直连通用方法：
```go
Add: func(up config.UpstreamConfig) (config.AddedUpstream, error) {
	return cfgManager.AddChannel("chat", up)   // 需把 addChannel 导出为 AddChannel
},
```
然后删除五组旧方法。**如果时间紧张，第二步可以留到以后，第一步已经拿到 ~700 行的收益。**

### 3.5 [x] 任务 P2-D：验证

```bash
cd backend-go && go build ./... && go vet ./... && go test ./...
```
`internal/config` 下已有的测试（`config_baseurl_test.go`、`config_pools_test.go`、`config_utils_test.go`）是本批最重要的安全网，**必须全绿**。特别关注：
- `TestAddUpstream_BaseURLDeduplication`
- `TestUpdateUpstream_BaseURLConsistency` / `TestUpdateGeminiUpstream_BaseURLConsistency` / `TestUpdateResponsesUpstream_BaseURLConsistency`
- `TestNormalizeUpstreamPrioritiesScopesByPool`

**预期收益**：净减约 600~700 行。

---

## 4. P3 · 协议 handler 骨架收敛

> 本批风险最高，**必须在 P0~P2 全部验证通过后才开始**。

### 4.1 问题陈述

`messages` / `responses` / `gemini` / `chat` / `images` 五个 handler 包，各自实现了同一套四段式流程，每份约 250 行：

```
Handler()                          → 认证 → 读 body → 解析 → 提取会话身份 →
                                     记录原始请求 → 解析指定渠道 → 判断多渠道
handleMultiChannel()               → common.HandleMultiChannelFailover(...)
handleSingleChannel()              → 取当前渠道 → 转 handleSingleChannelWithUpstream
handleSingleChannelWithUpstream()  → common.TryUpstreamWithModelMappingFailover(...)
```

`TryUpstreamWithModelMappingFailover` 有 15 个参数，其中 10 个在五份实现里是**逐字相同的闭包**（取下一个 key、降级 key、标记 URL 成败、AttemptLogContext 组装）。`messages/handler.go` 里甚至把同一组闭包在多渠道和单渠道两条路径上各写了一遍。

已经出现的行为分歧（说明复制粘贴正在腐化）：只有 messages 的单渠道路径调了 `channelScheduler.ConsumePromotionCount`，其余四个没有。

### 4.2 [x] 任务 P3-A：定义协议规格

新建 `backend-go/internal/handlers/common/protocol.go`：

```go
package common

// ProtocolSpec 描述一种代理协议的差异点。
// 五个协议共有的流程（认证/读体/会话观测/渠道选择/failover/日志）由 RunProxyRequest 承担，
// 协议只需填这张表。
type ProtocolSpec struct {
	Kind    scheduler.ChannelKind
	LogName string // 日志前缀，如 "Messages" / "Chat"

	// ParseRequest 解析请求体，返回本次请求的模型名、是否流式、提示词列表。
	// 解析失败时应自行写出 4xx 响应并返回 handled=false。
	ParseRequest func(c *gin.Context, body []byte) (model string, stream bool, prompts []string, ok bool)

	// BuildUpstreamRequest 构造发往上游的 http.Request。
	BuildUpstreamRequest func(c *gin.Context, up *config.UpstreamConfig, apiKey string, body []byte) (*http.Request, error)

	// HandleSuccess 处理上游 2xx 响应（含流式与非流式分派）。
	HandleSuccess func(c *gin.Context, resp *http.Response, up *config.UpstreamConfig, apiKey string, body []byte, startTime time.Time) (*types.Usage, error)

	// PreRoute 可选：协议特有的前置路由（如 chat 的 modelcatalog 路由、
	// responses 的 image passthrough）。返回 true 表示请求已被处理，主流程应直接返回。
	PreRoute func(c *gin.Context, body []byte, model string) bool
}
```

> 上面是**骨架**，实际字段以搬迁过程中的需要为准。允许增删字段，但每加一个字段都要问："这真的是协议差异，还是我没把共性抽干净？"

### 4.3 [x] 任务 P3-B：实现通用主流程

在同一文件实现 `RunProxyRequest(c *gin.Context, envCfg, cfgManager, sch, spec ProtocolSpec)`，把下列步骤搬进来（**以 `messages/handler.go` 的 `Handler` 为模板**，它最完整）：

1. `middleware.ProxyAuthMiddleware(envCfg)(c)` + `c.IsAborted()` 检查
2. `common.ReadRequestBody`
3. `spec.ParseRequest`
4. `common.ObserveConversationRequest` + `defer common.MarkConversationComplete`
5. `common.LogOriginalRequest`
6. `common.ExtractRequestedChannelIndex` + `common.ResolveRequestedUpstream`（指定渠道分支）
7. `spec.PreRoute`（若非 nil）
8. `sch.IsMultiChannelModeForModel` 分派到多渠道 / 单渠道
9. 两条路径都收敛到一个内部函数，统一组装 `TryUpstreamWithModelMappingFailover` 的 10 个公共闭包

**必须统一的既有分歧**：单渠道成功后一律调用 `sch.ConsumePromotionCount(channelIndex, kind)`（目前只有 messages 有）。这是本批**唯一**允许的行为变化，在提交说明里写明。

### 4.4 [x] 任务 P3-C：逐个协议迁移

**严格按此顺序，一次只迁一个，每迁完一个就 `go build` + `go test`**：

1. `images`（最简单，522 行，无会话逻辑）
2. `chat`（1055 行，注意保留 tool 清洗、`stripChatRoutingMetadata`、`ensureChatStreamUsageOptions` 等协议特有逻辑）
3. `messages`（513 行，模板来源）
4. `gemini`（716 行，注意 `ensureThoughtSignatures` / `stripThoughtSignature` / shadow store）
5. `responses`（1458 行，最复杂：session 链、compact 端点、image passthrough，**最后做**）

迁移时**留在协议包里**的东西：请求体清洗、模型映射、URL 拼接、响应解析与 usage 补全、协议特有的流式事件处理。**搬进 common 的**只有那四段骨架。

若某个协议（很可能是 `responses`）实在塞不进 `ProtocolSpec`，**允许它保持现状不迁**，在文档里记明原因即可——收敛 4 个也比硬套 5 个好。

> **执行记录（2026-08-04）**：`responses` 按计划保持现状不迁。原因：`handleSuccess` 需要 12 个参数（`provider`/`sessionManager`/`originalReq`/`hasImage`/`conversationID` 等），远超 `ProtocolSpec.HandleSuccess` 的 6 参签名；强行迁移需在 gin context 存放 3+ 个值并给 ProtocolSpec 加字段，风险集中在最复杂的流式/会话持久化路径。**已成功迁移 4 个协议：images / chat / messages / gemini**，全部通过 `go build` + `go vet` + `go test`。

### 4.5 [ ] 任务 P3-D：前端对应收敛

1. **合并 `frontend/src/services/api.ts` 的 `testChannel` 与 `testChannelWithModel`**
   两者 194 行 / 179 行，实测序列相似度 79%，各含一个 5 分支 `switch (apiType)`。
   做法：保留 `testChannelWithModel` 的签名为基础，把 `testChannel` 实现为 `testChannelWithModel(apiType, ..., { model: undefined })`；把两个 `switch (apiType)` 提取成一张模块级常量表：
   ```ts
   const PROTOCOL_TEST_ADAPTERS: Record<ApiTab, {
     buildBody: (model: string | undefined, prompt: string) => unknown
     parseChunk: (parsed: any) => string
   }> = { messages: {...}, responses: {...}, gemini: {...}, chat: {...}, images: {...} }
   ```

2. **`App.vue` 瘦身**（683 行 script）
   - 渠道写操作 `saveChannel` / `deleteChannel` / `pingChannel` / `pingAllChannels` 下沉到 `stores/channel.ts`（该 store 已存在且已被 20 处引用）
   - toast 系统（`toasts` / `showToast` / `getToastColor` / `getToastIcon` / `showErrorToast` / `showSuccessToast`）抽成 `composables/useToast.ts`
   - 目标：`App.vue` script 降到 300 行以内

3. **`ChannelOrchestration.vue` 拆分**（2344 行）
   把活动条相关的纯计算（`catmullRomToPath` / `getActivityPath` / `activityBarsCache` / `formatRPM`）抽到 `composables/useChannelActivity.ts`。
   > 拆分时注意 `const _ = activityUpdateTick.value` 这类响应式依赖必须跟着一起搬，否则图表会停止刷新。

### 4.6 [ ] 任务 P3-E：验证

```bash
cd backend-go && go build ./... && go vet ./... && go test ./...
cd ../frontend && bun run build
```
**必须人工冒烟测试**（本批改动了主链路，静态检查不够）：五个协议各发一次流式请求和一次非流式请求，确认响应正常、渠道指标有记录、对话列表有条目。

**预期收益**：后端净减约 900 行，前端净减约 250 行。

---

## 5. 遗留问题清单（发现即追加，不要就地修）

| 发现于 | 问题 | 处理建议 |
|---|---|---|
| P0 | `internal/metrics/channel_metrics.go` 单文件 2749 行 / 77 个函数，混合了记录、窗口统计、熔断、历史清理、聚合查询、持久化同步 | 独立一批按职责拆分为 4~5 个文件 |
| P0 | `converters` 存在新旧两套机制：旧 `ResponsesConverter` 接口 + 工厂，与新 `responses_protocol.go` 分发。目前只有 Claude 上游还走旧工厂 | 待 Claude 路径也迁到 protocol 分发后，整体删除旧接口 |
| P0 | `internal/visionlayer` 的 `TestTransformImagesForTargetProtocols/Gemini` 在 main 分支上即失败，`transformed payload text = ""` | 排查并修复 Gemini 图片转换逻辑 |
| P0 | `components/AddChannelModal.vue` script 达 1333 行 | 拆分为 快速添加 / 高级配置 / 模型映射 三个子组件 |

---

## 6. 验证记录

| 批次 | 执行日期 | `go build` | `go vet` | `go test` | 前端 build | 备注 |
|---|---|---|---|---|---|---|
| P0 | 2026-08-04 | ✅ | ✅ | ⚠️ visionlayer/Gemini 预置失败 | ✅ | 转入遗留问题清单 |
| P1 | 2026-08-04 | ✅ | ✅ | ✅ | ✅ | 净减约429行，gemini test 改用通用版 |
| P2 | 2026-08-04 | ✅ | ✅ | ✅ | N/A | 薄委托阶段，净减约314行，config测试全绿 |
| P3 | 2026-08-04 | ✅ | ✅ | ✅ | N/A | 4/5协议已迁移(images/chat/messages/gemini), responses按计划不迁 |
