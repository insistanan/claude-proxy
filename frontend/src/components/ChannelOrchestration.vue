<template>
  <v-card elevation="0" rounded="lg" class="channel-orchestration" variant="flat">
    <!-- 调度器统计信息 -->
    <v-card-title class="orchestration-header d-flex align-center justify-space-between py-3 px-0">
      <div class="d-flex align-center">
        <v-icon class="mr-2" color="primary">mdi-swap-vertical-bold</v-icon>
        <span class="text-h6">渠道编排</span>
        <v-chip v-if="isMultiChannelMode" size="small" color="success" variant="tonal" class="ml-3">
          多渠道模式
        </v-chip>
        <v-chip v-else size="small" color="warning" variant="tonal" class="ml-3"> 单渠道模式 </v-chip>
      </div>
      <div class="d-flex align-center ga-2">
        <v-progress-circular v-if="isLoadingMetrics" indeterminate size="16" width="2" color="primary" />
      </div>
    </v-card-title>

    <v-divider />

    <ChannelPoolGrid
      :channels="channels"
      :channel-type="channelType"
      @edit="$emit('edit', $event)"
      @delete="$emit('delete', $event)"
      @error="$emit('error', $event)"
      @success="$emit('success', $event)"
      @refresh="$emit('refresh')"
      @toggle-vision-status="toggleChannelPrimaryStatus"
    >
      <template #channel="{ channel: element, index, pool, move_to_top, move_to_bottom }">
          <div class="channel-item-wrapper">
            <div
              class="channel-row"
              :class="{ 'is-suspended': element.status === 'suspended' }"
              @click="toggleChannelChart(element.index)"
            >
              <!-- SVG 活跃度波形柱状图背景 -->
              <svg class="activity-chart-bg" preserveAspectRatio="none" viewBox="0 0 150 100">
                <!-- 渐变定义（为每个柱子单独定义渐变） -->
                <defs>
                  <linearGradient
                    v-for="(bar, i) in getActivityBars(element.index)"
                    :id="`gradient-${element.index}-${i}`"
                    :key="`gradient-${element.index}-${i}`"
                    x1="0%"
                    y1="0%"
                    x2="0%"
                    y2="100%"
                  >
                    <stop offset="0%" :stop-color="bar.color" stop-opacity="0.9" />
                    <stop offset="100%" :stop-color="bar.color" stop-opacity="0.4" />
                  </linearGradient>
                </defs>
                <!-- 波形柱状图 -->
                <g v-for="(bar, i) in getActivityBars(element.index)" :key="i">
                  <rect
                    :x="bar.x"
                    :y="bar.y"
                    :width="bar.width"
                    :height="bar.height"
                    :fill="`url(#gradient-${element.index}-${i})`"
                    :rx="bar.radius"
                    :ry="bar.radius"
                    class="activity-bar"
                    :class="[
                      `activity-bar-${bar.level}`,
                      { 'activity-bar-latest': i === getActivityBars(element.index).length - 1 }
                    ]"
                  />
                </g>
              </svg>

              <!-- Grid 内容容器 -->
              <div class="channel-row-content">
                <!-- 拖拽手柄 -->
                <div class="drag-handle" @click.stop>
                  <v-icon size="small" color="grey">mdi-drag-vertical</v-icon>
                </div>

            <!-- 优先级序号 -->
            <div class="priority-number" @click.stop>
              <span class="text-caption font-weight-bold">{{ index + 1 }}</span>
            </div>

            <!-- 状态指示器 -->
            <div
              class="channel-status-toggle"
              role="button"
              tabindex="0"
              title="点击切换活跃或熔断"
              @click.stop="toggleChannelPrimaryStatus(element)"
              @keydown.enter.stop="toggleChannelPrimaryStatus(element)"
              @keydown.space.prevent.stop="toggleChannelPrimaryStatus(element)"
            >
              <ChannelStatusBadge :status="element.status || 'active'" :metrics="getChannelMetrics(element.index)" />
            </div>

            <!-- 渠道名称和描述 -->
            <div class="channel-name">
              <span
                class="font-weight-medium channel-name-link"
                tabindex="0"
                role="button"
                @click.stop="$emit('edit', element)"
                @keydown.enter.stop="$emit('edit', element)"
                @keydown.space.stop="$emit('edit', element)"
              >{{ element.name }}</span>
              <!-- 促销期标识 -->
              <v-chip
                v-if="isInPromotion(element)"
                size="x-small"
                color="info"
                variant="flat"
                class="ml-2"
              >
                <v-icon start size="12">mdi-rocket-launch</v-icon>
                {{ formatPromotionRemaining(element.promotionUntil, element.promotionCount) }}
              </v-chip>
              <v-chip
                v-if="element.temporary"
                size="x-small"
                color="warning"
                variant="tonal"
                class="ml-2"
              >
                <v-icon start size="12">mdi-timer-sand</v-icon>
                临时 {{ formatDateTime(element.temporaryUntil) }}
              </v-chip>
              <v-tooltip
                v-if="shouldShowVisionCapability(element)"
                text="支持图片理解"
                location="top"
                :open-delay="150"
                :open-on-focus="false"
              >
                <template #activator="{ props: tooltipProps }">
                  <v-chip
                    v-bind="tooltipProps"
                    size="x-small"
                    color="primary"
                    variant="flat"
                    class="ml-2 vision-default-chip"
                    @click.stop
                  >
                    <v-icon start size="12">mdi-image-search-outline</v-icon>
                    图片
                  </v-chip>
                </template>
              </v-tooltip>
              <!-- 官网链接按钮 -->
              <v-btn
                :href="getWebsiteUrl(element)"
                target="_blank"
                rel="noopener"
                icon
                size="x-small"
                variant="text"
                color="primary"
                class="ml-1"
                title="打开官网"
                @click.stop
              >
                <v-icon size="14">mdi-open-in-new</v-icon>
              </v-btn>
              <span class="text-caption text-medium-emphasis ml-2">{{ element.serviceType }}</span>
              <v-chip
                v-if="formatChannelModelPreview(element)"
                size="x-small"
                color="secondary"
                variant="tonal"
                class="ml-2 model-preview-chip"
                :title="formatChannelModelPreview(element)"
              >
                {{ formatChannelModelPreview(element) }}
              </v-chip>
              <span v-if="element.description" class="text-caption text-disabled ml-3 channel-description">{{ element.description }}</span>
              <!-- 展开图标 -->
              <v-icon
                size="x-small"
                class="ml-auto expand-icon"
                :color="expandedChannelIndex === element.index ? 'primary' : 'grey-lighten-1'"
                @click.stop="toggleChannelChart(element.index)"
              >{{ expandedChannelIndex === element.index ? 'mdi-chevron-up' : 'mdi-chevron-down' }}</v-icon>
            </div>

            <!-- 指标显示 — 视觉条 -->
            <div class="channel-metrics" @click.stop>
              <template v-if="getChannelMetrics(element.index)">
                <v-tooltip location="top" :open-delay="200" :open-on-focus="false">
                  <template #activator="{ props: tooltipProps }">
                    <div v-bind="tooltipProps" class="metrics-visual">
                      <!-- 15分钟有请求时显示指标条，否则显示 -- -->
                      <template v-if="get15mStats(element.index)?.requestCount">
                        <!-- 成功率条形图 -->
                        <div class="mini-metric-bar">
                          <div class="mmb-track">
                            <div
                              class="mmb-fill"
                              :class="getRateLevel(get15mStats(element.index)?.successRate)"
                              :style="{ width: `${get15mStats(element.index)?.successRate ?? 0}%` }"
                            ></div>
                            <!-- 指标刷新时重放一次扫光 -->
                            <span
                              :key="`mf-${get15mStats(element.index)?.successRate?.toFixed(0)}`"
                              class="mmb-flash"
                            ></span>
                          </div>
                          <span
                            class="mmb-value"
                            :key="`mmb-${get15mStats(element.index)?.successRate?.toFixed(0)}`"
                            :class="getRateLevel(get15mStats(element.index)?.successRate)"
                          >
                            {{ get15mStats(element.index)?.successRate?.toFixed(0) }}%
                          </span>
                        </div>
                        <!-- 请求数 + 缓存命中率 -->
                        <div class="mini-metric-secondary">
                          <span class="mm-requests">{{ get15mStats(element.index)?.requestCount }} 请求</span>
                          <template v-if="shouldShowCacheHitRate(get15mStats(element.index))">
                            <span class="mm-sep">|</span>
                            <span class="mm-cache">缓存 {{ getCacheHitRate(get15mStats(element.index))?.toFixed(0) }}%</span>
                          </template>
                        </div>
                      </template>
                      <span v-else class="text-caption text-medium-emphasis">--</span>
                    </div>
                  </template>
                  <div class="metrics-tooltip">
                    <div class="text-caption font-weight-bold mb-1">请求统计</div>
                    <div class="metrics-tooltip-row">
                      <span>15分钟:</span>
                      <span>{{ formatStats(get15mStats(element.index)) }}</span>
                    </div>
                    <div class="metrics-tooltip-row">
                      <span>1小时:</span>
                      <span>{{ formatStats(get1hStats(element.index)) }}</span>
                    </div>
                    <div class="metrics-tooltip-row">
                      <span>6小时:</span>
                      <span>{{ formatStats(get6hStats(element.index)) }}</span>
                    </div>
                    <div class="metrics-tooltip-row">
                      <span>24小时:</span>
                      <span>{{ formatStats(get24hStats(element.index)) }}</span>
                    </div>

                    <div class="text-caption font-weight-bold mt-2 mb-1">缓存统计 (Token)</div>
                    <div class="metrics-tooltip-row">
                      <span>15分钟:</span>
                      <span>{{ formatCacheStats(get15mStats(element.index)) }}</span>
                    </div>
                    <div class="metrics-tooltip-row">
                      <span>1小时:</span>
                      <span>{{ formatCacheStats(get1hStats(element.index)) }}</span>
                    </div>
                    <div class="metrics-tooltip-row">
                      <span>6小时:</span>
                      <span>{{ formatCacheStats(get6hStats(element.index)) }}</span>
                    </div>
                    <div class="metrics-tooltip-row">
                      <span>24小时:</span>
                      <span>{{ formatCacheStats(get24hStats(element.index)) }}</span>
                    </div>
                  </div>
                </v-tooltip>
              </template>
              <span v-else class="text-caption text-medium-emphasis">--</span>
            </div>

            <!-- RPM/TPM 显示 -->
            <div class="channel-rpm-tpm" @click.stop>
              <div class="rpm-tpm-values">
                <span class="rpm-value" :class="{ 'has-data': hasActivityData(element.index) }">{{ formatRPM(element.index) }}</span>
                <span class="rpm-tpm-separator">/</span>
                <span class="tpm-value" :class="{ 'has-data': hasActivityData(element.index) }">{{ formatTPM(element.index) }}</span>
              </div>
              <div class="rpm-tpm-labels">
                <span>RPM</span>
                <span>/</span>
                <span>TPM</span>
              </div>
            </div>

            <!-- 延迟显示 -->
            <div class="channel-latency" @click.stop>
              <v-chip
                v-if="isLatencyValid(element)"
                size="x-small"
                :color="getLatencyColor(element.latency!)"
                variant="tonal"
              >
                {{ element.latency }}ms
              </v-chip>
            </div>

            <!-- API密钥数量 -->
            <div class="channel-keys" @click.stop>
              <v-chip size="x-small" variant="outlined" class="keys-chip" @click="$emit('edit', element)">
                <v-icon start size="x-small">mdi-key</v-icon>
                {{ element.apiKeys?.length || 0 }}
              </v-chip>
            </div>

            <!-- 操作按钮 -->
            <div class="channel-actions" @click.stop>
              <!-- suspended 状态显示恢复按钮 -->
              <v-btn
                v-if="element.status === 'suspended'"
                icon
                size="x-small"
                variant="text"
                color="warning"
                title="恢复"
                @click="resumeChannel(element.index)"
              >
                <v-icon size="small">mdi-refresh</v-icon>
              </v-btn>

              <v-menu>
                <template #activator="{ props: menuProps }">
                  <v-btn icon size="x-small" variant="text" v-bind="menuProps">
                    <v-icon size="small">mdi-dots-vertical</v-icon>
                  </v-btn>
                </template>
                <v-list density="compact">
                  <v-list-item @click="$emit('edit', element)">
                    <template #prepend>
                      <v-icon size="small">mdi-pencil</v-icon>
                    </template>
                    <v-list-item-title>编辑</v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="duplicateChannel(element.index)">
                    <template #prepend>
                      <v-icon size="small">mdi-content-copy</v-icon>
                    </template>
                    <v-list-item-title>复制渠道</v-list-item-title>
                  </v-list-item>
                  <v-list-item v-if="supportsVisionCapability" @click="toggleVisionCapability(element)">
                    <template #prepend>
                      <v-icon size="small" :color="element.visionCapable ? 'success' : 'primary'">
                        {{ element.visionCapable ? 'mdi-check-circle' : 'mdi-image-search-outline' }}
                      </v-icon>
                    </template>
                    <v-list-item-title>
                      {{ element.visionCapable ? '取消支持图片理解' : '设为支持图片理解' }}
                    </v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="copyChannelConfig(element)">
                    <template #prepend>
                      <v-icon size="small" :color="copiedChannelIndex === element.index ? 'success' : 'primary'">
                        {{ copiedChannelIndex === element.index ? 'mdi-check' : 'mdi-content-copy' }}
                      </v-icon>
                    </template>
                    <v-list-item-title>
                      {{ copiedChannelIndex === element.index ? '已复制配置' : '复制配置' }}
                    </v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="handleQuickTest(element)">
                    <template #prepend>
                      <v-icon size="small" color="success">mdi-test-tube</v-icon>
                    </template>
                    <v-list-item-title>快捷测试</v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="$emit('ping', element.index)">
                    <template #prepend>
                      <v-icon size="small">mdi-speedometer</v-icon>
                    </template>
                    <v-list-item-title>测试延迟</v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="openLogsDialog(element)">
                    <template #prepend>
                      <v-icon size="small">mdi-text-box-search-outline</v-icon>
                    </template>
                    <v-list-item-title>请求日志</v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="openPromotionDialog(element)">
                    <template #prepend>
                      <v-icon size="small" color="info">mdi-rocket-launch</v-icon>
                    </template>
                    <v-list-item-title>抢优先级</v-list-item-title>
                  </v-list-item>
                  <v-list-item v-if="index > 0" @click="move_to_top()">
                    <template #prepend>
                      <v-icon size="small" color="primary">mdi-arrow-collapse-up</v-icon>
                    </template>
                    <v-list-item-title>置顶</v-list-item-title>
                  </v-list-item>
                  <v-list-item v-if="index < pool.channels.length - 1" @click="move_to_bottom()">
                    <template #prepend>
                      <v-icon size="small" color="primary">mdi-arrow-collapse-down</v-icon>
                    </template>
                    <v-list-item-title>置底</v-list-item-title>
                  </v-list-item>
                  <v-divider />
                  <v-list-item v-if="element.status === 'suspended'" @click="resumeChannel(element.index)">
                    <template #prepend>
                      <v-icon size="small" color="success">mdi-play-circle</v-icon>
                    </template>
                    <v-list-item-title>恢复 (重置指标)</v-list-item-title>
                  </v-list-item>
                  <v-list-item
                    v-if="element.status !== 'suspended'"
                    @click="setChannelStatus(element.index, 'suspended')"
                  >
                    <template #prepend>
                      <v-icon size="small" color="warning">mdi-pause-circle</v-icon>
                    </template>
                    <v-list-item-title>暂停</v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="setChannelStatus(element.index, 'disabled')">
                    <template #prepend>
                      <v-icon size="small" color="error">mdi-stop-circle</v-icon>
                    </template>
                    <v-list-item-title>移至备用池</v-list-item-title>
                  </v-list-item>
                  <v-list-item @click="setChannelStatus(element.index, 'deprecated')">
                    <template #prepend>
                      <v-icon size="small" color="grey">mdi-archive-clock-outline</v-icon>
                    </template>
                    <v-list-item-title>移至弃用池</v-list-item-title>
                  </v-list-item>
                  <v-list-item :disabled="!canDeleteChannel(element)" @click="handleDeleteChannel(element)">
                    <template #prepend>
                      <v-icon size="small" :color="canDeleteChannel(element) ? 'error' : 'grey'">mdi-delete</v-icon>
                    </template>
                    <v-list-item-title>
                      删除
                      <span v-if="!canDeleteChannel(element)" class="text-caption text-disabled ml-1">
                        (至少保留一个)
                      </span>
                    </v-list-item-title>
                  </v-list-item>
                </v-list>
              </v-menu>
            </div>
              </div><!-- .channel-row-content -->
          </div><!-- .channel-row -->

          <!-- 展开的图表区域 -->
          <v-expand-transition>
            <div v-if="expandedChannelIndex === element.index" class="channel-chart-wrapper">
              <KeyTrendChart
                :key="`chart-${channelType}-${element.index}`"
                :channel-id="element.index"
                :channel-type="channelType"
                @close="expandedChannelIndex = null"
              />
            </div>
          </v-expand-transition>
          </div>
      </template>
    </ChannelPoolGrid>

    <v-divider class="my-2" />

    <!-- 备用资源池 (disabled only) -->
    <div class="pt-2 pb-3">
      <div class="inactive-pool-header">
        <div class="text-subtitle-2 text-medium-emphasis d-flex align-center">
          <v-icon size="small" class="mr-1" color="grey">mdi-archive-outline</v-icon>
          备用资源池
          <v-chip size="x-small" class="ml-2">{{ inactiveChannels.length }}</v-chip>
        </div>
        <span class="text-caption text-medium-emphasis">启用后将追加到活跃序列末尾</span>
      </div>

      <div v-if="inactiveChannels.length > 0" class="inactive-pool">
        <div v-for="channel in inactiveChannels" :key="channel.index" class="inactive-channel-row">
          <!-- 渠道信息 -->
          <div class="channel-info">
            <div class="channel-info-main">
              <span
                class="font-weight-medium channel-name-link"
                tabindex="0"
                role="button"
                @click="$emit('edit', channel)"
                @keydown.enter="$emit('edit', channel)"
                @keydown.space.prevent="$emit('edit', channel)"
              >{{ channel.name }}</span>
              <span class="text-caption text-disabled ml-2">{{ channel.serviceType }}</span>
              <v-chip
                v-if="formatChannelModelPreview(channel)"
                size="x-small"
                color="secondary"
                variant="tonal"
                class="ml-2 model-preview-chip"
                :title="formatChannelModelPreview(channel)"
              >
                {{ formatChannelModelPreview(channel) }}
              </v-chip>
              <v-chip v-if="channel.temporary" size="x-small" color="warning" variant="tonal" class="ml-2">
                临时 {{ formatDateTime(channel.temporaryUntil) }}
              </v-chip>
              <v-tooltip
                v-if="shouldShowVisionCapability(channel)"
                text="支持图片理解"
                location="top"
                :open-delay="150"
                :open-on-focus="false"
              >
                <template #activator="{ props: tooltipProps }">
                  <v-chip
                    v-bind="tooltipProps"
                    size="x-small"
                    color="primary"
                    variant="flat"
                    class="ml-2 vision-default-chip"
                  >
                    <v-icon start size="12">mdi-image-search-outline</v-icon>
                    图片
                  </v-chip>
                </template>
              </v-tooltip>
            </div>
            <div v-if="channel.description" class="channel-info-desc text-caption text-disabled">
              {{ channel.description }}
            </div>
          </div>

          <!-- API密钥数量 -->
          <div class="channel-keys">
            <v-chip size="x-small" variant="outlined" color="grey" class="keys-chip" @click="$emit('edit', channel)">
              <v-icon start size="x-small">mdi-key</v-icon>
              {{ channel.apiKeys?.length || 0 }}
            </v-chip>
          </div>

          <!-- 操作按钮 -->
          <div class="channel-actions">
            <v-btn size="small" color="success" variant="tonal" @click="enableChannel(channel.index)">
              <v-icon start size="small">mdi-play-circle</v-icon>
              启用
            </v-btn>

            <v-menu>
              <template #activator="{ props: menuProps }">
                <v-btn icon size="x-small" variant="text" v-bind="menuProps">
                  <v-icon size="small">mdi-dots-vertical</v-icon>
                </v-btn>
              </template>
              <v-list density="compact">
                <v-list-item @click="$emit('edit', channel)">
                  <template #prepend>
                    <v-icon size="small">mdi-pencil</v-icon>
                  </template>
                  <v-list-item-title>编辑</v-list-item-title>
                </v-list-item>
                <v-list-item @click="duplicateChannel(channel.index)">
                  <template #prepend>
                    <v-icon size="small">mdi-content-copy</v-icon>
                  </template>
                  <v-list-item-title>复制渠道</v-list-item-title>
                </v-list-item>
                <v-list-item v-if="supportsVisionCapability" @click="toggleVisionCapability(channel)">
                  <template #prepend>
                    <v-icon size="small" :color="channel.visionCapable ? 'success' : 'primary'">
                      {{ channel.visionCapable ? 'mdi-check-circle' : 'mdi-image-search-outline' }}
                    </v-icon>
                  </template>
                  <v-list-item-title>
                    {{ channel.visionCapable ? '取消支持图片理解' : '设为支持图片理解' }}
                  </v-list-item-title>
                </v-list-item>
                <v-divider />
                <v-list-item @click="enableChannel(channel.index)">
                  <template #prepend>
                    <v-icon size="small" color="success">mdi-play-circle</v-icon>
                  </template>
                  <v-list-item-title>启用</v-list-item-title>
                </v-list-item>
                <v-list-item @click="setChannelStatus(channel.index, 'deprecated')">
                  <template #prepend>
                    <v-icon size="small" color="grey">mdi-archive-clock-outline</v-icon>
                  </template>
                  <v-list-item-title>移至弃用池</v-list-item-title>
                </v-list-item>
                <v-list-item @click="$emit('delete', channel.index)">
                  <template #prepend>
                    <v-icon size="small" color="error">mdi-delete</v-icon>
                  </template>
                  <v-list-item-title>删除</v-list-item-title>
                </v-list-item>
              </v-list>
            </v-menu>
          </div>
        </div>
      </div>

      <div v-else class="text-center py-4 text-medium-emphasis text-caption">所有渠道都处于活跃状态</div>
    </div>

    <v-divider class="my-2" />

    <!-- 弃用渠道池 (deprecated only) -->
    <div class="pt-2 pb-3">
      <div class="inactive-pool-header">
        <div class="text-subtitle-2 text-medium-emphasis d-flex align-center">
          <v-icon size="small" class="mr-1" color="grey">mdi-archive-clock-outline</v-icon>
          弃用渠道池
          <v-chip size="x-small" class="ml-2">{{ deprecatedChannels.length }}</v-chip>
        </div>
        <span class="text-caption text-medium-emphasis">每 12 小时清理一次，弃用超过 3 天自动删除</span>
      </div>

      <div v-if="deprecatedChannels.length > 0" class="inactive-pool deprecated-pool">
        <div v-for="channel in deprecatedChannels" :key="channel.index" class="inactive-channel-row">
          <div class="channel-info">
            <div class="channel-info-main">
              <span
                class="font-weight-medium channel-name-link"
                tabindex="0"
                role="button"
                @click="$emit('edit', channel)"
                @keydown.enter="$emit('edit', channel)"
                @keydown.space.prevent="$emit('edit', channel)"
              >{{ channel.name }}</span>
              <span class="text-caption text-disabled ml-2">{{ channel.serviceType }}</span>
              <v-chip
                v-if="formatChannelModelPreview(channel)"
                size="x-small"
                color="secondary"
                variant="tonal"
                class="ml-2 model-preview-chip"
                :title="formatChannelModelPreview(channel)"
              >
                {{ formatChannelModelPreview(channel) }}
              </v-chip>
            </div>
            <div class="channel-info-desc text-caption text-disabled">
              弃用时间：{{ formatDateTime(channel.deprecatedAt) }}
            </div>
          </div>

          <div class="channel-keys">
            <v-chip size="x-small" variant="outlined" color="grey" class="keys-chip" @click="$emit('edit', channel)">
              <v-icon start size="x-small">mdi-key</v-icon>
              {{ channel.apiKeys?.length || 0 }}
            </v-chip>
          </div>

          <div class="channel-actions">
            <v-btn size="small" color="grey" variant="tonal" @click="setChannelStatus(channel.index, 'disabled')">
              <v-icon start size="small">mdi-archive-outline</v-icon>
              转备用
            </v-btn>
            <v-btn size="small" color="error" variant="text" @click="$emit('delete', channel.index)">
              <v-icon start size="small">mdi-delete</v-icon>
              删除
            </v-btn>
          </div>
        </div>
      </div>

      <div v-else class="text-center py-4 text-medium-emphasis text-caption">暂无弃用渠道</div>
    </div>
  </v-card>

    <v-dialog v-model="showPromotionDialog" max-width="420">
      <v-card>
        <v-card-title class="d-flex align-center">
          <v-icon color="info" class="mr-2">mdi-rocket-launch</v-icon>
          设置促销期
          <span v-if="promotionChannel" class="ml-2 text-subtitle-1 text-medium-emphasis"> — {{ promotionChannel.name }}</span>
        </v-card-title>
        <v-divider />
        <v-card-text class="pt-4">
          <p class="text-body-2 text-medium-emphasis mb-4">
            促销期渠道将绕过健康检查，获得最高调度优先级。可同时设置时间和次数限制，任一条件满足即自动结束。
          </p>
          <v-text-field
            v-model.number="promotionDuration"
            label="促销时长（分钟）"
            type="number"
            :min="0"
            hint="0 = 不限时长"
            persistent-hint
            density="compact"
            class="mb-3"
          />
          <v-text-field
            v-model.number="promotionCount"
            label="促销请求次数"
            type="number"
            :min="0"
            hint="0 = 不限次数，每次成功请求自动减1"
            persistent-hint
            density="compact"
          />
        </v-card-text>
        <v-divider />
        <v-card-actions>
          <v-spacer />
          <v-btn variant="text" @click="closePromotionDialog">取消</v-btn>
          <v-btn color="info" variant="flat" @click="confirmPromotion">确认设置</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>

    <v-dialog v-model="showLogsDialog" max-width="1280">
      <v-card class="channel-logs-dialog-card">
        <v-card-title class="channel-logs-title">
          <div class="channel-logs-title-main">
            <v-icon size="20" color="primary">mdi-text-box-search-outline</v-icon>
            <div class="channel-logs-title-text">
              <span>{{ logsChannel?.name || '渠道' }} 请求日志</span>
              <span class="channel-logs-subtitle">{{ channelLogs.length }} 条记录</span>
            </div>
          </div>
          <v-btn icon variant="text" size="small" @click="closeLogsDialog">
            <v-icon>mdi-close</v-icon>
          </v-btn>
        </v-card-title>
        <v-divider />
        <v-card-text class="channel-logs-dialog-body">
          <div v-if="isLoadingLogs" class="d-flex align-center justify-center py-8">
            <v-progress-circular indeterminate color="primary" />
          </div>
          <v-alert v-else-if="logsError" type="error" variant="tonal" density="compact">
            {{ logsError }}
          </v-alert>
          <div v-else>
            <div v-if="channelLogs.length > 0" class="log-trend-panel">
              <div class="log-trend-header">
                <div class="text-subtitle-2 font-weight-bold">调用用量趋势</div>
                <div class="text-caption text-medium-emphasis">
                  按分钟聚合，展示请求数、Token I/O 和缓存 R/W
                </div>
              </div>
              <apexchart
                type="line"
                height="220"
                :options="logTrendChartOptions"
                :series="logTrendSeries"
              />
            </div>

            <div class="channel-logs-table-shell">
              <v-table density="compact" class="channel-logs-table">
                <colgroup>
                  <col class="log-col-time" />
                  <col class="log-col-status" />
                  <col class="log-col-model" />
                  <col class="log-col-token" />
                  <col class="log-col-cache" />
                  <col class="log-col-upstream" />
                  <col class="log-col-key" />
                  <col class="log-col-duration" />
                  <col class="log-col-error" />
                </colgroup>
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>状态</th>
                    <th>模型</th>
                    <th>Token I/O</th>
                    <th>缓存 R/W</th>
                    <th>上游</th>
                    <th>Key</th>
                    <th>耗时</th>
                    <th>失败明细</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-if="channelLogs.length === 0">
                    <td colspan="9" class="text-center text-medium-emphasis py-6">暂无请求日志</td>
                  </tr>
                  <tr v-for="log in channelLogs" :key="log.attemptId">
                    <td class="log-time-cell">{{ formatLogTime(log.timestamp) }}</td>
                    <td>
                      <v-chip size="x-small" :color="getLogStatusColor(log.status)" variant="tonal">
                        {{ formatLogStatus(log.status, log.statusCode) }}
                      </v-chip>
                    </td>
                    <td>
                      <div class="log-model" :title="log.model || '--'">{{ log.model || '--' }}</div>
                    </td>
                    <td class="log-number-cell">
                      {{ formatLogTokenPair(log.inputTokens, log.outputTokens) }}
                    </td>
                    <td class="log-number-cell">
                      <div>{{ formatLogTokenPair(log.cacheCreationTokens, log.cacheReadTokens) }}</div>
                      <div v-if="(log.cacheCreation5mTokens || 0) + (log.cacheCreation1hTokens || 0) > 0" class="log-cell-note">
                        5m {{ formatNumber(log.cacheCreation5mTokens) }} / 1h {{ formatNumber(log.cacheCreation1hTokens) }}
                      </div>
                    </td>
                    <td>
                      <div class="log-base-url" :title="log.baseUrl">{{ formatLogBaseUrl(log.baseUrl) }}</div>
                    </td>
                    <td class="log-key-cell">{{ log.keyMask }}</td>
                    <td class="log-duration-cell">{{ formatDurationMs(log.durationMs) }}</td>
                    <td>
                      <div v-if="log.errorType || log.errorMessage" class="log-error" :title="formatLogError(log)">
                        <div class="log-error-type">{{ log.errorType || 'error' }}</div>
                        <div class="log-error-message">{{ log.errorMessage || '--' }}</div>
                      </div>
                      <span v-else class="text-medium-emphasis">--</span>
                    </td>
                  </tr>
                </tbody>
              </v-table>
            </div>
          </div>
        </v-card-text>
      </v-card>
    </v-dialog>

    <!-- 快捷测试弹窗 -->
    <QuickTestModal
      v-model="showQuickTestModal"
      :channel="quickTestChannel"
      :api-type="channelType"
    />

</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import VueApexCharts from 'vue3-apexcharts'
import type { ApexOptions } from 'apexcharts'
import { api, channelApiByType, type Channel, type ChannelMetrics, type ChannelStatus, type TimeWindowStats, type ChannelRecentActivity, type ChannelLogEntry } from '../services/api'
import ChannelStatusBadge from './ChannelStatusBadge.vue'
import ChannelPoolGrid from './ChannelPoolGrid.vue'
import KeyTrendChart from './KeyTrendChart.vue'
import QuickTestModal from './QuickTestModal.vue'

const apexchart = VueApexCharts

const props = defineProps<{
  channels: Channel[]
  currentChannelIndex: number
  channelType: 'messages' | 'responses' | 'gemini' | 'chat' | 'images'
  // 可选：从父组件传入的 metrics 和 stats（使用 dashboard 接口时）
  dashboardMetrics?: ChannelMetrics[]
  dashboardStats?: {
    multiChannelMode: boolean
    activeChannelCount: number
    traceAffinityCount: number
    traceAffinityTTL: string
    failureThreshold: number
    windowSize: number
    circuitRecoveryTime?: string
  }
  // 可选：从父组件传入的实时活跃度数据
  dashboardRecentActivity?: ChannelRecentActivity[]
}>()

const emit = defineEmits<{
  (_e: 'edit', _channel: Channel): void
  (_e: 'delete', _channelId: number): void
  (_e: 'ping', _channelId: number): void
  (_e: 'quickTest', _channelId: number): void
  (_e: 'refresh'): void
  (_e: 'error', _message: string): void
  (_e: 'success', _message: string): void
}>()

const supportsVisionCapability = computed(() => true)

// 快捷测试弹窗状态
const showQuickTestModal = ref(false)
const quickTestChannel = ref<Channel | null>(null)

// 状态
const metrics = ref<ChannelMetrics[]>([])
const recentActivity = ref<ChannelRecentActivity[]>([])
const schedulerStats = ref<{
  multiChannelMode: boolean
  activeChannelCount: number
  traceAffinityCount: number
  traceAffinityTTL: string
  failureThreshold: number
  windowSize: number
} | null>(null)
const isLoadingMetrics = ref(false)

const showLogsDialog = ref(false)
const logsChannel = ref<Channel | null>(null)
const channelLogs = ref<ChannelLogEntry[]>([])
const isLoadingLogs = ref(false)
const logsError = ref('')

type LogTrendBucket = {
  x: number
  requests: number
  inputTokens: number
  outputTokens: number
  cacheCreationTokens: number
  cacheReadTokens: number
}

const logTrendBuckets = computed<LogTrendBucket[]>(() => {
  const buckets = new Map<number, LogTrendBucket>()
  for (const log of channelLogs.value) {
    const timestamp = new Date(log.timestamp).getTime()
    if (!Number.isFinite(timestamp)) continue
    const bucketTime = Math.floor(timestamp / 60000) * 60000
    const bucket = buckets.get(bucketTime) ?? {
      x: bucketTime,
      requests: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheCreationTokens: 0,
      cacheReadTokens: 0
    }

    bucket.requests += 1
    bucket.inputTokens += log.inputTokens ?? 0
    bucket.outputTokens += log.outputTokens ?? 0
    bucket.cacheCreationTokens += log.cacheCreationTokens ?? 0
    bucket.cacheReadTokens += log.cacheReadTokens ?? 0
    buckets.set(bucketTime, bucket)
  }

  return Array.from(buckets.values()).sort((a, b) => a.x - b.x)
})

const logTrendSeries = computed(() => {
  const points = logTrendBuckets.value
  return [
    { name: '输入Token', data: points.map(point => [point.x, point.inputTokens]) },
    { name: '输出Token', data: points.map(point => [point.x, point.outputTokens]) },
    { name: '缓存创建Token', data: points.map(point => [point.x, point.cacheCreationTokens]) },
    { name: '缓存读取Token', data: points.map(point => [point.x, point.cacheReadTokens]) },
    { name: '请求数', data: points.map(point => [point.x, point.requests]) }
  ]
})

const logTrendChartOptions = computed<ApexOptions>(() => ({
  chart: {
    toolbar: { show: false },
    zoom: { enabled: false },
    animations: { enabled: false },
    fontFamily: 'inherit'
  },
  colors: ['#5C6BC8', '#2FA478', '#C1804A', '#8A72C0', '#7B8494'],
  dataLabels: { enabled: false },
  stroke: {
    width: [2, 2, 2, 2, 2],
    curve: 'smooth'
  },
  markers: {
    size: 3,
    strokeWidth: 0
  },
  grid: {
    borderColor: 'rgba(128, 134, 148, 0.24)',
    strokeDashArray: 4
  },
  xaxis: {
    type: 'datetime',
    labels: {
      datetimeUTC: false
    }
  },
  yaxis: [
    {
      seriesName: ['输入Token', '输出Token', '缓存创建Token', '缓存读取Token'],
      title: { text: 'Tokens' },
      labels: { formatter: value => formatCompactNumber(value) }
    },
    {
      seriesName: '请求数',
      opposite: true,
      title: { text: '请求数' },
      labels: { formatter: value => formatCompactNumber(value) }
    }
  ],
  tooltip: {
    shared: true,
    x: { format: 'HH:mm:ss' },
    y: { formatter: value => formatNumber(value) }
  },
  legend: {
    position: 'bottom',
    fontSize: '12px'
  }
}))

// 促销弹窗相关
const showPromotionDialog = ref(false)
const promotionChannel = ref<Channel | null>(null)
const promotionDuration = ref(5) // 默认 5 分钟
const promotionCount = ref(0) // 默认 0 = 不限制次数

// 延迟测试结果有效期（5 分钟）
const LATENCY_VALID_DURATION = 5 * 60 * 1000
// 用于触发响应式更新的时间戳
const currentTime = ref(Date.now())
let latencyCheckTimer: ReturnType<typeof setInterval> | null = null

// 用于触发活跃度视图更新的时间戳（每 2 秒更新）
const activityUpdateTick = ref(0)
let activityUpdateTimer: ReturnType<typeof setInterval> | null = null

// 图表展开状态
const expandedChannelIndex = ref<number | null>(null)

// 切换渠道图表展开/收起
const toggleChannelChart = (channelIndex: number) => {
  expandedChannelIndex.value = expandedChannelIndex.value === channelIndex ? null : channelIndex
}

const handleQuickTest = (channel: Channel) => {
  quickTestChannel.value = channel
  showQuickTestModal.value = true
}

// 复制渠道配置到剪贴板
const copiedChannelIndex = ref<number | null>(null)
const copyChannelConfig = async (channel: Channel) => {
  try {
    // 获取第一个密钥
    const firstKey = channel.apiKeys.length > 0 ? channel.apiKeys[0] : ''
    
    // 构建配置文本
    const configText = `名称: ${channel.name}
URL: ${channel.baseUrl}
密钥: ${firstKey}`
    
    // 复制到剪贴板
    await navigator.clipboard.writeText(configText)
    
    // 显示已复制状态
    copiedChannelIndex.value = channel.index
    setTimeout(() => {
      copiedChannelIndex.value = null
    }, 2000)
  } catch (error) {
    console.error('复制配置失败:', error)
  }
}

// 计算属性：非活跃渠道 - 仅 disabled 状态
const inactiveChannels = computed(() => {
	return props.channels.filter(ch => !ch.excludeFromConversation && ch.status === 'disabled')
})

const deprecatedChannels = computed(() => {
	return props.channels.filter(ch => !ch.excludeFromConversation && ch.status === 'deprecated')
})

// 计算属性：是否为多渠道模式
// 多渠道模式判断逻辑：
// 1. 只有一个启用的渠道 → 单渠道模式
// 2. 有一个 active + 几个 suspended → 单渠道模式
// 3. 有多个 active 渠道 → 多渠道模式
const isMultiChannelMode = computed(() => {
	const activeCount = props.channels.filter(
		ch => !ch.excludeFromConversation && (ch.status === 'active' || ch.status === undefined || ch.status === '')
	).length
  return activeCount > 1
})

// 监听 dashboard props 变化（从父组件传入的合并数据）
watch(() => props.dashboardMetrics, (newMetrics) => {
  if (newMetrics) {
    metrics.value = newMetrics
  }
}, { immediate: true })

watch(() => props.dashboardStats, (newStats) => {
  if (newStats) {
    schedulerStats.value = newStats
  }
}, { immediate: true })

// 监听 recentActivity props 变化
watch(() => props.dashboardRecentActivity, (newActivity) => {
  recentActivity.value = newActivity ?? []
}, { immediate: true })

// 监听 channelType 变化 - 切换时刷新指标并收起图表
watch(() => props.channelType, () => {
  expandedChannelIndex.value = null // 收起展开的图表
  // 如果没有使用 dashboard props，则自己刷新
  if (!props.dashboardMetrics) {
    refreshMetrics()
  }
})

// 获取渠道指标
const getChannelMetrics = (channelIndex: number): ChannelMetrics | undefined => {
  return metrics.value.find(m => m.channelIndex === channelIndex)
}

// 获取分时段统计的辅助方法
const get15mStats = (channelIndex: number) => {
  return getChannelMetrics(channelIndex)?.timeWindows?.['15m']
}

const get1hStats = (channelIndex: number) => {
  return getChannelMetrics(channelIndex)?.timeWindows?.['1h']
}

const get6hStats = (channelIndex: number) => {
  return getChannelMetrics(channelIndex)?.timeWindows?.['6h']
}

const get24hStats = (channelIndex: number) => {
  return getChannelMetrics(channelIndex)?.timeWindows?.['24h']
}

// 获取成功率颜色
const getSuccessRateColor = (rate?: number): string => {
  if (rate === undefined) return 'grey'
  if (rate >= 90) return 'success'
  if (rate >= 70) return 'warning'
  return 'error'
}

const getCacheHitRateColor = (rate?: number): string => {
  if (rate === undefined) return 'grey'
  if (rate >= 50) return 'success'
  if (rate >= 20) return 'info'
  if (rate >= 5) return 'warning'
  return 'orange'
}

const getRateLevel = (rate?: number): string => {
  if (rate === undefined || rate === null) return 'unknown'
  if (rate >= 90) return 'high'
  if (rate >= 70) return 'medium'
  return 'low'
}

const getCacheHitRate = (stats?: TimeWindowStats): number | undefined => {
  if (!stats) return undefined
  const inputTokens = stats.inputTokens ?? 0
  const cacheReadTokens = stats.cacheReadTokens ?? 0
  const denom = inputTokens + cacheReadTokens

  if (denom <= 0) return undefined
  return stats.cacheHitRate ?? (cacheReadTokens / denom * 100)
}

const shouldShowCacheHitRate = (stats?: TimeWindowStats): boolean => {
  if (!stats || !stats.requestCount) return false
  return getCacheHitRate(stats) !== undefined
}

// 获取延迟颜色
const getLatencyColor = (latency: number): string => {
  if (latency < 500) return 'success'
  if (latency < 1000) return 'warning'
  return 'error'
}

// 判断延迟测试结果是否仍然有效（5 分钟内）
const isLatencyValid = (channel: Channel): boolean => {
  // 没有延迟值，不显示
  if (channel.latency === undefined || channel.latency === null) return false
  // 没有测试时间戳（兼容旧数据），不显示
  if (!channel.latencyTestTime) return false
  // 检查是否在有效期内（使用 currentTime.value 触发响应式更新）
  return (currentTime.value - channel.latencyTestTime) < LATENCY_VALID_DURATION
}

// 判断渠道是否处于促销期
const isInPromotion = (channel: Channel): boolean => {
  if (channel.promotionCount && channel.promotionCount > 0) return true
  if (!channel.promotionUntil) return false
  return new Date(channel.promotionUntil) > new Date()
}

// 格式化促销期剩余时间
const formatPromotionRemaining = (until?: string, count?: number): string => {
  if (count && count > 0) return `剩 ${count} 次`
  if (!until) return ''
  const remaining = Math.max(0, new Date(until).getTime() - Date.now())
  const minutes = Math.ceil(remaining / 60000)
  if (minutes <= 0) return '即将结束'
  return `${minutes}分钟`
}

const shouldShowVisionCapability = (channel: Channel): boolean => {
  return supportsVisionCapability.value && !!channel.visionCapable
}

// 格式化统计数据：有请求显示"N 请求 (X%)"，无请求显示"--"
const formatStats = (stats?: TimeWindowStats): string => {
  if (!stats || !stats.requestCount) return '--'
  return `${stats.requestCount} 请求 (${stats.successRate?.toFixed(0)}%)`
}

const formatTokens = (num?: number): string => {
  const value = num ?? 0
  if (value >= 1000000) return `${(value / 1000000).toFixed(1)}M`
  if (value >= 1000) return `${(value / 1000).toFixed(1)}K`
  return Math.round(value).toString()
}

const formatCacheStats = (stats?: TimeWindowStats): string => {
  if (!stats || !stats.requestCount) return '--'

  const inputTokens = stats.inputTokens ?? 0
  const cacheReadTokens = stats.cacheReadTokens ?? 0
  const cacheCreationTokens = stats.cacheCreationTokens ?? 0
  const denom = inputTokens + cacheReadTokens

  if (denom <= 0) return '--'

  const hitRate = getCacheHitRate(stats)
  if (hitRate === undefined) return '--'
  return `命中 ${hitRate.toFixed(0)}% · 读 ${formatTokens(cacheReadTokens)} · 写 ${formatTokens(cacheCreationTokens)}`
}

// 获取官网 URL（优先使用 website，否则从 baseUrl 提取域名）
const getWebsiteUrl = (channel: Channel): string => {
  if (channel.website) return channel.website
  try {
    const url = new URL(channel.baseUrl)
    return `${url.protocol}//${url.host}`
  } catch {
    return channel.baseUrl
  }
}

const toggleVisionCapability = async (channel: Channel) => {
  const nextValue = !channel.visionCapable

  try {
    await channelApiByType(props.channelType).updateChannel(channel.index, { visionCapable: nextValue })

    emit('refresh')
    emit('success', nextValue
      ? `已将 ${channel.name} 设为支持图片理解`
      : `已取消 ${channel.name} 的图片理解支持`)
  } catch (error) {
    console.error('Failed to update vision capability:', error)
    const errorMessage = error instanceof Error ? error.message : '未知错误'
    emit('error', `设置图片理解能力失败: ${errorMessage}`)
  }
}

// ============== 渠道实时活跃度相关函数 ==============

// 活跃度数据 Map 缓存（避免线性查找）
const activityMap = computed(() => {
  const map = new Map<number, ChannelRecentActivity>()
  for (const a of recentActivity.value) {
    map.set(a.channelIndex, a)
  }
  return map
})

// 每个渠道的历史最大请求数（用于固定柱状图高度比例）
const maxRequestsHistory = ref(new Map<number, number>())

// 更新历史最大值
watch(activityMap, (newMap) => {
  for (const [channelIndex, activity] of newMap.entries()) {
    if (!activity.segments || activity.segments.length === 0) continue

    const currentMax = Math.max(...activity.segments.map(s => s.requestCount), 0)
    const historicalMax = maxRequestsHistory.value.get(channelIndex) ?? 0

    // 只在当前最大值更大时更新（保持历史峰值）
    if (currentMax > historicalMax) {
      maxRequestsHistory.value.set(channelIndex, currentMax)
    }
  }
})

// 获取渠道的活跃度数据
const getChannelActivity = (channelIndex: number): ChannelRecentActivity | undefined => {
  return activityMap.value.get(channelIndex)
}

// 波形柱的健康档位 — 驱动 CSS 动效强度（越差动得越急）
type ActivityLevel = 'idle' | 'ok' | 'warn' | 'bad'
type ActivityBar = {
  x: number
  y: number
  width: number
  height: number
  radius: number
  color: string
  level: ActivityLevel
}

// 缓存所有渠道的柱状图数据（避免在模板中重复计算）
const activityBarsCache = computed(() => {
  const cache = new Map<number, ActivityBar[]>()

  // 使用 activityUpdateTick 触发响应式更新
  const _ = activityUpdateTick.value

  for (const [channelIndex, activity] of activityMap.value.entries()) {
    if (!activity || !activity.segments || activity.segments.length === 0) {
      cache.set(channelIndex, [])
      continue
    }

    const segments = activity.segments
    const numSegments = segments.length  // 150（后端已聚合为每 6 秒一段）

    // 每个段一个柱子
    const barWidth = 150 / numSegments
    const barGap = barWidth * 0.2  // 20% 间隙
    const actualBarWidth = barWidth - barGap

    // 使用历史最大值作为归一化基准（避免高流量段离开后柱子突然变高）
    const maxRequests = maxRequestsHistory.value.get(channelIndex) ?? Math.max(...segments.map(s => s.requestCount), 1)

    const bars: ActivityBar[] = []

    for (let i = 0; i < numSegments; i++) {
      const segment = segments[i]
      const requests = segment.requestCount

      // 计算柱子高度（最小高度 2，避免完全消失）
      const heightPercent = requests / maxRequests
      const height = Math.max(heightPercent * 85, requests > 0 ? 2 : 0)
      const y = 100 - height

      // 根据该 6 秒段的成功率计算颜色（7 档分级：极端档位 + 整数档位）
      let color = 'rgb(52, 211, 153)'  // 默认绿色（无请求或 100% 成功）
      // 动效档位：柱子越"红"动得越急，故障在余光里也能被察觉
      let level: ActivityLevel = 'idle'

      if (requests > 0) {
        const successCount = requests - segment.failureCount
        const successRate = (successCount / requests) * 100

        if (successRate < 5) {
          color = 'rgb(220, 38, 38)'       // 0-5%：深红色（极端故障）
          level = 'bad'
        } else if (successRate < 20) {
          color = 'rgb(248, 113, 113)'     // 5-20%：红色（严重失败）
          level = 'bad'
        } else if (successRate < 40) {
          color = 'rgb(251, 146, 60)'      // 20-40%：深橙色（高失败率）
          level = 'bad'
        } else if (successRate < 60) {
          color = 'rgb(250, 204, 21)'      // 40-60%：黄色（中等失败率）
          level = 'warn'
        } else if (successRate < 80) {
          color = 'rgb(163, 230, 53)'      // 60-80%：黄绿色（轻微失败）
          level = 'warn'
        } else if (successRate < 95) {
          color = 'rgb(74, 222, 128)'      // 80-95%：亮绿色（良好）
          level = 'ok'
        } else {
          color = 'rgb(52, 211, 153)'      // 95-100%：翠绿色（优秀）
          level = 'ok'
        }
      }

      bars.push({
        x: i * barWidth + barGap / 2,
        y,
        width: actualBarWidth,
        height,
        radius: Math.min(actualBarWidth / 2, 1.5),  // 圆角半径
        color,
        level
      })
    }

    cache.set(channelIndex, bars)
  }

  return cache
})

// 生成波形柱状图数据（从缓存中读取）
const getActivityBars = (channelIndex: number): ActivityBar[] => {
  return activityBarsCache.value.get(channelIndex) ?? []
}

// 生成平滑曲线路径（使用移动平均 + Catmull-Rom 样条）
const getActivityPath = (channelIndex: number): string => {
  const activity = getChannelActivity(channelIndex)
  if (!activity || !activity.segments || activity.segments.length === 0) return ''

  // 使用 activityUpdateTick 触发响应式更新
   
  const _ = activityUpdateTick.value

  const segments = activity.segments
  const numSegments = segments.length  // 150（后端已聚合为每 6 秒一段）

  // 找到最大请求数用于归一化
  const maxRequests = Math.max(...segments.map(s => s.requestCount), 1)

  // 应用移动平均平滑数据（窗口大小 5 = 10 秒）
  const windowSize = 5
  const smoothedData: number[] = []

  for (let i = 0; i < numSegments; i++) {
    const start = Math.max(0, i - Math.floor(windowSize / 2))
    const end = Math.min(numSegments, i + Math.ceil(windowSize / 2))
    let sum = 0
    let count = 0

    for (let j = start; j < end; j++) {
      sum += segments[j].requestCount
      count++
    }

    smoothedData.push(count > 0 ? sum / count : 0)
  }

  // 生成平滑后的点
  const points: { x: number; y: number }[] = []
  for (let i = 0; i < numSegments; i++) {
    const x = i
    const y = 100 - (smoothedData[i] / maxRequests * 85)
    points.push({ x, y })
  }

  if (points.length < 2) return ''

  // 使用 Catmull-Rom 样条生成平滑曲线
  return catmullRomToPath(points)
}

// Catmull-Rom 样条转 SVG 贝塞尔路径
function catmullRomToPath(points: { x: number; y: number }[]): string {
  if (points.length < 2) return ''

  const path: string[] = []
  path.push(`M ${points[0].x} ${points[0].y}`)

  // 张力参数（0.3 = 较低张力，曲线更贴近原始点）
  const tension = 0.3

  for (let i = 0; i < points.length - 1; i++) {
    const p0 = points[Math.max(0, i - 1)]
    const p1 = points[i]
    const p2 = points[i + 1]
    const p3 = points[Math.min(points.length - 1, i + 2)]

    // 计算控制点
    const cp1x = p1.x + (p2.x - p0.x) / 6 * tension
    const cp1y = p1.y + (p2.y - p0.y) / 6 * tension
    const cp2x = p2.x - (p3.x - p1.x) / 6 * tension
    const cp2y = p2.y - (p3.y - p1.y) / 6 * tension

    path.push(`C ${cp1x} ${cp1y}, ${cp2x} ${cp2y}, ${p2.x} ${p2.y}`)
  }

  return path.join(' ')
}

// 格式化 RPM 显示
const formatRPM = (channelIndex: number): string => {
  const activity = getChannelActivity(channelIndex)
  if (!activity || activity.rpm === 0) return '--'
  if (activity.rpm >= 10) return activity.rpm.toFixed(0)
  return activity.rpm.toFixed(1)
}

// 格式化 TPM 显示
const formatTPM = (channelIndex: number): string => {
  const activity = getChannelActivity(channelIndex)
  if (!activity || activity.tpm === 0) return '--'
  if (activity.tpm >= 1000000) return `${(activity.tpm / 1000000).toFixed(1)}M`
  if (activity.tpm >= 1000) return `${(activity.tpm / 1000).toFixed(1)}K`
  return activity.tpm.toFixed(0)
}

// 判断渠道是否有活跃度数据
const hasActivityData = (channelIndex: number): boolean => {
  const activity = getChannelActivity(channelIndex)
  if (!activity) return false
  return activity.rpm > 0 || activity.tpm > 0
}

// 刷新指标
const refreshMetrics = async () => {
  isLoadingMetrics.value = true
  try {
    const [metricsData, statsData] = await Promise.all([
      channelApiByType(props.channelType).getChannelMetrics(),
      api.getSchedulerStats(props.channelType)
    ])
    metrics.value = metricsData
    schedulerStats.value = statsData
  } catch (error) {
    console.error('Failed to load metrics:', error)
  } finally {
    isLoadingMetrics.value = false
  }
}

const duplicateChannel = async (channelIndex: number) => {
  try {
    await api.duplicateChannel(props.channelType, channelIndex)
    emit('refresh')
  } catch (error) {
    console.error('Failed to duplicate channel:', error)
    const errorMessage = error instanceof Error ? error.message : '未知错误'
    emit('error', `复制渠道失败: ${errorMessage}`)
  }
}

// 设置渠道状态
const setChannelStatus = async (channelId: number, status: ChannelStatus) => {
  try {
    await channelApiByType(props.channelType).setStatus(channelId, status)
    emit('refresh')
  } catch (error) {
    console.error('Failed to set channel status:', error)
    const errorMessage = error instanceof Error ? error.message : '未知错误'
    emit('error', `设置渠道状态失败: ${errorMessage}`)
  }
}

// 启用渠道（从备用池移到活跃序列）
const enableChannel = async (channelId: number) => {
  await setChannelStatus(channelId, 'active')
}

// 恢复渠道（重置指标并设为 active）
const resumeChannel = async (channelId: number) => {
  try {
    await channelApiByType(props.channelType).resumeChannel(channelId)
    await setChannelStatus(channelId, 'active')
  } catch (error) {
    console.error('Failed to resume channel:', error)
  }
}

const toggleChannelPrimaryStatus = async (channel: Channel) => {
  const currentStatus = channel.status || 'active'

  if (currentStatus === 'suspended') {
    await resumeChannel(channel.index)
    return
  }

  if (currentStatus === 'active') {
    await setChannelStatus(channel.index, 'suspended')
  }
}

const openPromotionDialog = (channel: Channel) => {
  promotionChannel.value = channel
  promotionDuration.value = 5
  promotionCount.value = 0
  showPromotionDialog.value = true
}

const closePromotionDialog = () => {
  showPromotionDialog.value = false
  promotionChannel.value = null
}

const openLogsDialog = async (channel: Channel) => {
  logsChannel.value = channel
  showLogsDialog.value = true
  logsError.value = ''
  channelLogs.value = []
  isLoadingLogs.value = true
  try {
    const response = await api.getChannelLogs(props.channelType, channel.index)
    channelLogs.value = response.logs || []
  } catch (error) {
    console.error('Failed to load channel logs:', error)
    logsError.value = error instanceof Error ? error.message : '加载请求日志失败'
  } finally {
    isLoadingLogs.value = false
  }
}

const closeLogsDialog = () => {
  showLogsDialog.value = false
  logsChannel.value = null
  channelLogs.value = []
  logsError.value = ''
}

const formatLogTime = (value: string): string => {
  if (!value) return '--'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

const formatDateTime = (value?: string): string => {
  if (!value) return '--'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

const formatChannelModelPreview = (channel: Channel): string => {
  const defaultModel = String(channel.defaultModel || '').trim()
  if (defaultModel) {
    return `兜底 ${defaultModel}`
  }

  const entries = normalizeModelMappingEntries(channel.modelMapping)
  if (entries.length === 0) return ''

  const preferred = pickPreferredModelMapping(entries, channel)
  if (!preferred) return ''
  const [source, target] = preferred
  return source === target ? target : `${source} -> ${target}`
}

const normalizeModelMappingEntries = (
  mapping?: Record<string, string[]>
): Array<readonly [string, string]> => {
  return Object.entries(mapping || {})
    .flatMap(([source, targets]) => {
      const cleanSource = source.trim()
      const targetList = Array.isArray(targets) ? targets : [targets]
      return targetList
        .map(target => [cleanSource, String(target || '').trim()] as const)
        .filter(([cleanSource, target]) => cleanSource && target)
    })
}

const pickPreferredModelMapping = (
  entries: Array<readonly [string, string]>,
  channel: Channel
): readonly [string, string] | undefined => {
  const lowerService = channel.serviceType.toLowerCase()
  const preferredTerms = props.channelType === 'messages' || lowerService === 'claude'
    ? ['opus', 'sonnet', 'claude']
    : props.channelType === 'responses' || props.channelType === 'images' || lowerService === 'responses' || lowerService === 'openai' || lowerService === 'chat'
      ? ['gpt', 'codex']
      : ['gemini']

  for (const term of preferredTerms) {
    const match = entries.find(([source]) => source.toLowerCase().includes(term))
    if (match) return match
  }

  return [...entries].sort((a, b) => a[0].localeCompare(b[0]))[0]
}

const getLogStatusColor = (status: string): string => {
  if (status === 'completed') return 'success'
  if (status === 'cancelled') return 'grey'
  return 'error'
}

const formatLogStatus = (status: string, statusCode?: number): string => {
  const labels: Record<string, string> = {
    completed: '成功',
    failed: '失败',
    cancelled: '取消'
  }
  const label = labels[status] || status || '--'
  return statusCode ? `${label} ${statusCode}` : label
}

const formatLogBaseUrl = (value?: string): string => {
  const raw = String(value || '').trim()
  if (!raw) return '--'
  try {
    const url = new URL(raw)
    const path = url.pathname && url.pathname !== '/' ? url.pathname : ''
    return `${url.host}${path}`
  } catch {
    return raw
  }
}

const formatDurationMs = (value?: number): string => {
  const numeric = Number(value ?? 0)
  if (!Number.isFinite(numeric) || numeric <= 0) return '0ms'
  if (numeric >= 1000) return `${(numeric / 1000).toFixed(numeric >= 10000 ? 0 : 1)}s`
  return `${Math.round(numeric)}ms`
}

const formatLogError = (log: ChannelLogEntry): string => {
  return [log.errorType, log.errorMessage].filter(Boolean).join('\n') || '--'
}

const formatNumber = (value?: number): string => {
  const numeric = Number(value ?? 0)
  if (!Number.isFinite(numeric)) return '0'
  return new Intl.NumberFormat().format(Math.round(numeric))
}

const formatCompactNumber = (value?: number): string => {
  const numeric = Number(value ?? 0)
  if (!Number.isFinite(numeric)) return '0'
  return new Intl.NumberFormat(undefined, {
    notation: 'compact',
    maximumFractionDigits: 1
  }).format(numeric)
}

const formatLogTokenPair = (left?: number, right?: number): string => {
  return `${formatNumber(left)} / ${formatNumber(right)}`
}

const normalizePromotionInput = (value: unknown): number => {
  const parsed = typeof value === 'number'
    ? value
    : Number(String(value ?? '').trim())

  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0
  }

  return Math.floor(parsed)
}

const confirmPromotion = async () => {
  if (!promotionChannel.value) return
  const channel = promotionChannel.value
  const durationMinutes = normalizePromotionInput(promotionDuration.value)
  const count = normalizePromotionInput(promotionCount.value)

  promotionDuration.value = durationMinutes
  promotionCount.value = count

  const durationSeconds = durationMinutes * 60

  try {
    if (channel.status === 'suspended') {
      await channelApiByType(props.channelType).resumeChannel(channel.index)
      await setChannelStatus(channel.index, 'active')
    }

    await channelApiByType(props.channelType).setPromotion(channel.index, durationSeconds, count)
    emit('refresh')
    const durationText = durationMinutes > 0 ? `${durationMinutes}分钟内` : ''
    const countText = count > 0 ? `${count}次请求内` : ''
    const combined = [durationText, countText].filter(Boolean).join(' / ')
    emit('success', `渠道 ${channel.name} 已设为最高优先级${combined ? '（' + combined + '）' : ''}`)
  } catch (error) {
    console.error('Failed to set promotion:', error)
    const errorMessage = error instanceof Error ? error.message : '未知错误'
    emit('error', `设置优先级失败: ${errorMessage}`)
  } finally {
    closePromotionDialog()
  }
}

// 判断渠道是否可以删除
// 规则：当前模型路由子池中至少要保留一个 active 状态的渠道
const canDeleteChannel = (channel: Channel): boolean => {
  // 统计当前 active 状态的渠道数量
  const poolID = channel.poolId || 'default'
  const activeCount = props.channels.filter(
    ch => !ch.excludeFromConversation && (ch.poolId || 'default') === poolID &&
      (ch.status === 'active' || ch.status === undefined || ch.status === '')
  ).length

  // 如果要删除的是 active 渠道，且只剩一个 active，则不允许删除
  const isActive = channel.status === 'active' || channel.status === undefined || channel.status === ''
  if (isActive && activeCount <= 1) {
    return false
  }

  return true
}

// 处理删除渠道
const handleDeleteChannel = (channel: Channel) => {
  if (!canDeleteChannel(channel)) {
    emit('error', '无法删除：当前模型路由子池中至少需要保留一个活跃渠道')
    return
  }
  emit('delete', channel.index)
}

// 组件挂载时加载指标并启动延迟过期检查定时器
onMounted(() => {
  refreshMetrics()
  // 每 30 秒更新一次 currentTime，触发延迟显示的响应式更新
  latencyCheckTimer = setInterval(() => {
    currentTime.value = Date.now()
  }, 30000)
  // 每 2 秒更新一次 activityUpdateTick，触发活跃度视图更新
  activityUpdateTimer = setInterval(() => {
    activityUpdateTick.value++
  }, 2000)
})

// 组件卸载时清理定时器
onUnmounted(() => {
  if (latencyCheckTimer) {
    clearInterval(latencyCheckTimer)
    latencyCheckTimer = null
  }
  if (activityUpdateTimer) {
    clearInterval(activityUpdateTimer)
    activityUpdateTimer = null
  }
})

// 暴露方法给父组件
defineExpose({
  refreshMetrics
})
</script>

<style scoped>
/* ============================================================
   API Proxy — 调度轨道
   编号、状态边线、数据波形组成统一的设备语言
   ============================================================ */

.channel-orchestration { overflow: visible; background: transparent; border: none; }
.orchestration-header {
  min-height: 58px;
  margin-bottom: 16px;
  border-bottom: 2px solid rgba(var(--v-theme-outline), 0.55);
  position: relative;
  overflow: visible;
}
/* 标题下的主色标尺 — 带刻度，像面板上的量程条 */
.orchestration-header::after {
  content: '';
  position: absolute;
  left: 0;
  bottom: -2px;
  width: clamp(110px, 18vw, 240px);
  height: 3px;
  background:
    repeating-linear-gradient(90deg,
      rgba(255, 255, 255, 0.55) 0 1px,
      transparent 1px 8px),
    rgb(var(--v-theme-primary));
}
.channel-list { display: flex; flex-direction: column; gap: 8px; }
.channel-item-wrapper { display: flex; flex-direction: column; }

.channel-row {
  position: relative;
  padding: 12px 16px;
  background: rgb(var(--v-theme-surface));
  border: 1px solid rgba(var(--v-theme-outline), 0.6);
  /* 对角不对称切角 — 全站统一的形状签名 */
  border-radius: var(--cut-md) var(--cut-xs) var(--cut-md) var(--cut-xs);
  box-shadow: var(--shadow-1);
  min-height: 52px;
  transition: transform 0.16s var(--ease-out), box-shadow 0.16s ease, border-color 0.16s ease;
  cursor: pointer;
  overflow: hidden;
}

/* 左侧刻度状态轨 */
.channel-row::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  bottom: 0;
  width: var(--rail);
  background:
    repeating-linear-gradient(180deg,
      rgba(255, 255, 255, 0.5) 0 1px,
      transparent 1px 8px),
    linear-gradient(180deg,
      rgb(var(--v-theme-primary)) 0%,
      rgba(var(--v-theme-primary), 0.42) 50%,
      rgb(var(--v-theme-primary)) 100%);
  background-size: 100% 100%, 100% 200%;
  box-shadow: inset -1px 0 0 rgba(0, 0, 0, 0.14);
  z-index: 2;
}

/* 横向扫描线 — 仅 hover 时扫过，避免整屏几十行同时闪动 */
.channel-row::after {
  content: '';
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 40%;
  background: linear-gradient(90deg, transparent, rgba(var(--v-theme-primary), 0.1), transparent);
  pointer-events: none;
  opacity: 0;
  animation: scan-sweep 2.6s ease-in-out infinite;
  animation-play-state: paused;
  z-index: 3;
}
.channel-row:hover::after { opacity: 1; animation-play-state: running; }
.channel-row:hover::before { animation: rail-flow 3s linear infinite; }

.channel-row-content {
  display: grid;
  grid-template-columns: 28px 28px 90px minmax(140px, 1fr) auto 60px 60px 60px auto;
  align-items: center;
  gap: 8px;
  position: relative;
  z-index: 1;
}

.channel-row:hover {
  border-color: rgba(var(--v-theme-primary), 0.7);
  box-shadow: var(--shadow-2);
  transform: translateY(-2px);
}

.channel-row:active { transform: translateY(0); box-shadow: var(--shadow-1); }

/* suspended 状态 — 琥珀底 + 琥珀轨，与活跃行拉开明显反差 */
.channel-row.is-suspended {
  background: color-mix(in srgb, rgb(var(--v-theme-warning)) 9%, rgb(var(--v-theme-surface)));
  border-color: rgba(var(--v-theme-warning), 0.6);
}
.channel-row.is-suspended:hover { border-color: rgba(var(--v-theme-warning), 0.85); }
.channel-row.is-suspended::before {
  background:
    repeating-linear-gradient(180deg,
      rgba(255, 255, 255, 0.5) 0 1px,
      transparent 1px 8px),
    rgb(var(--v-theme-warning));
}

.channel-row.ghost {
  opacity: 0.5;
  background: rgba(var(--v-theme-primary), 0.06);
  border: 2px dashed rgba(var(--v-theme-primary), 0.45);
}

/* SVG 活跃度波形背景 */
.activity-chart-bg {
  position: absolute; top: 0; left: 0; width: 100%; height: 100%;
  pointer-events: none; z-index: 0; opacity: 0.45; overflow: hidden;
  border-radius: inherit;
  transition: opacity 0.2s ease;
}
.channel-row:hover .activity-chart-bg { opacity: 0.62; }

/* 柱子生长 — 从底部竖起（首次渲染播放一次） */
.activity-bar {
  transition: none;
  transform-box: fill-box;
  transform-origin: center bottom;
  animation: bar-rise 0.5s var(--ease-out) both;
}

/* 分档动效：健康档静止，越差抖得越急 —— 余光扫过就能发现故障段 */
.activity-bar-warn {
  animation: bar-rise 0.5s var(--ease-out) both, spark-blink 2.4s ease-in-out infinite;
}
.activity-bar-bad {
  animation: bar-rise 0.5s var(--ease-out) both, spark-blink 1.05s ease-in-out infinite;
}

/* 最新柱 — 高亮并持续闪烁，标记"当前实时" */
.activity-bar-latest {
  filter: brightness(1.45) saturate(1.2);
  animation: bar-rise 0.5s var(--ease-out) both, led-blink 1.6s ease-in-out infinite;
}

.channel-chart-wrapper { margin: 4px 0 8px 0; }

.drag-handle {
  cursor: grab; display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; border-radius: 5px;
  transition: all 0.1s ease;
}
.drag-handle:hover { background: rgba(var(--v-theme-on-surface), 0.06); }
.drag-handle:active { cursor: grabbing; background: rgba(var(--v-theme-primary), 0.1); }

.priority-number {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px;
  background: rgb(var(--v-theme-primary));
  color: rgb(var(--v-theme-on-primary));
  font-size: 11px; font-weight: 800;
  font-family: 'Fira Code', 'JetBrains Mono', monospace;
  border: none;
  /* 与卡片同向的不对称切角 */
  border-radius: 6px 2px 6px 2px;
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.25);
}

.channel-name {
  display: flex; align-items: center; overflow: hidden; gap: 4px;
}
.channel-name .expand-icon { flex-shrink: 0; margin-left: auto; }
.channel-name .font-weight-medium { font-size: 0.9rem; flex-shrink: 0; }

.channel-description {
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden; text-overflow: ellipsis; line-height: 1.4;
  max-height: calc(1.4em * 2); word-break: break-word;
  font-size: 0.78rem; opacity: 0.5;
}

.channel-name-link { cursor: pointer; transition: color 0.15s ease; }
.channel-name-link:hover, .channel-name-link:focus { color: rgb(var(--v-theme-primary)); }
.channel-name-link:focus-visible { outline: 2px solid rgb(var(--v-theme-primary)); outline-offset: 2px; border-radius: 4px; }

.channel-status-toggle {
  display: inline-flex; align-items: center; border-radius: 3px; cursor: pointer;
}
.channel-status-toggle:focus-visible { outline: 2px solid rgb(var(--v-theme-primary)); outline-offset: 2px; }
.channel-status-toggle :deep(.badge-content) { cursor: pointer; }

.channel-metrics { display: flex; align-items: center; gap: 6px; flex-wrap: nowrap; white-space: nowrap; min-width: 140px; }

/* ===== 迷你指标条 — 与渠道卡同一套仪表语言的压缩版 ===== */
.metrics-visual { min-width: 130px; }
.mini-metric-bar { display: flex; align-items: center; gap: 7px; margin-bottom: 2px; }

/* 凹槽轨道 + 等距刻度 */
.mmb-track {
  flex: 1;
  height: 9px;
  min-width: 60px;
  border-radius: 2px;
  position: relative;
  overflow: hidden;
  background:
    repeating-linear-gradient(90deg,
      rgba(var(--v-theme-on-surface), 0.13) 0 1px,
      transparent 1px var(--tick)),
    rgba(var(--v-theme-outline), 0.24);
  box-shadow: inset 0 1px 2px rgba(0, 0, 0, 0.14);
}
/* 90% 健康阈值刻线 */
.mmb-track::after {
  content: '';
  position: absolute;
  left: 90%; top: 0; bottom: 0;
  width: 1px;
  background: rgba(var(--v-theme-on-surface), 0.4);
  z-index: 3;
}
.v-theme--dark .mmb-track {
  background:
    repeating-linear-gradient(90deg,
      rgba(255, 255, 255, 0.09) 0 1px,
      transparent 1px var(--tick)),
    rgba(0, 0, 0, 0.4);
  box-shadow: inset 0 1px 3px rgba(0, 0, 0, 0.55);
}
.v-theme--dark .mmb-track::after { background: rgba(255, 255, 255, 0.3); }

.mmb-fill {
  height: 100%;
  border-radius: 2px;
  transition: width 0.85s var(--ease-spring);
  min-width: 3px;
  position: relative;
  z-index: 1;
}

/* 波头 — 末端示波器亮点 */
.mmb-fill::before {
  content: '';
  position: absolute;
  right: -1px;
  top: 50%;
  transform: translateY(-50%);
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.92);
  box-shadow: 0 0 8px 2px currentColor;
  animation: wave-head 2.6s ease-in-out infinite;
}

/* 流光 — 数据在流动 */
.mmb-fill::after {
  content: '';
  position: absolute;
  top: 0; bottom: 0; left: 0;
  width: 55%;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.36), transparent);
  animation: shimmer-slide 3.2s ease-in-out infinite both;
}

/* 指标刷新扫光 */
.mmb-flash {
  position: absolute;
  inset: 0;
  z-index: 2;
  pointer-events: none;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.5), transparent);
  animation: shimmer-slide 0.62s var(--ease-out) 1 both;
}
.v-theme--dark .mmb-flash { background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.3), transparent); }

/* 健康（绿）— 平稳 */
.mmb-fill.high {
  background: linear-gradient(90deg, rgba(var(--v-theme-success), 0.72), rgb(var(--v-theme-success)));
  color: rgb(var(--v-theme-success));
  box-shadow: 0 0 8px rgba(var(--v-theme-success), 0.4), inset 0 1px 0 rgba(255, 255, 255, 0.3);
}
/* 亚健康（黄）— 节奏加快 */
.mmb-fill.medium {
  background: linear-gradient(90deg, rgba(var(--v-theme-warning), 0.72), rgb(var(--v-theme-warning)));
  color: rgb(var(--v-theme-warning));
  box-shadow: 0 0 8px rgba(var(--v-theme-warning), 0.42), inset 0 1px 0 rgba(255, 255, 255, 0.28);
}
.mmb-fill.medium::before { animation-duration: 1.8s; }
.mmb-fill.medium::after { animation-duration: 2.2s; }
/* 故障（红）— 滚动警示斜纹 + 急促红光呼吸 */
.mmb-fill.low {
  background:
    repeating-linear-gradient(135deg,
      rgba(0, 0, 0, 0.26) 0 5px,
      transparent 5px 10px),
    linear-gradient(90deg, rgba(var(--v-theme-error), 0.75), rgb(var(--v-theme-error)));
  background-size: 20px 100%, 100% 100%;
  color: rgb(var(--v-theme-error));
  animation: stripe-drift 0.85s linear infinite, alarm-breathe 1.5s ease-in-out infinite;
}
.mmb-fill.low::before { animation-duration: 1.05s; }
.mmb-fill.low::after { display: none; }

.mmb-value {
  font-size: 11px; font-weight: 700;
  font-family: 'Fira Code', 'JetBrains Mono', monospace;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  animation: value-pop 0.45s var(--ease-spring);
}
.mmb-value.high { color: rgb(var(--v-theme-success)); }
.mmb-value.medium { color: rgb(var(--v-theme-warning)); }
.mmb-value.low { color: rgb(var(--v-theme-error)); }

.mini-metric-secondary {
  display: flex; align-items: center; gap: 4px;
  font-size: 10px;
  font-family: 'Fira Code', 'JetBrains Mono', monospace;
  color: rgba(var(--v-theme-on-surface-variant), 0.8);
}
.mm-sep { opacity: 0.35; }
.channel-latency { display: flex; align-items: center; min-width: 60px; }

.channel-rpm-tpm { display: flex; flex-direction: column; align-items: center; min-width: 60px; }
.rpm-tpm-values {
  display: flex; align-items: baseline; gap: 2px;
  font-size: 13px; font-weight: 700;
  font-family: 'Fira Code', 'JetBrains Mono', monospace;
  font-variant-numeric: tabular-nums;
  color: rgba(var(--v-theme-on-surface), 0.4);
}
.rpm-tpm-values .rpm-value.has-data, .rpm-tpm-values .tpm-value.has-data { color: rgb(var(--v-theme-primary)); }
.rpm-tpm-separator { color: rgba(var(--v-theme-on-surface), 0.2); font-weight: 400; }
.rpm-tpm-labels { display: flex; align-items: center; gap: 2px; font-size: 9px; color: rgba(var(--v-theme-on-surface), 0.4); text-transform: uppercase; letter-spacing: 0.06em; }

.channel-keys { display: flex; align-items: center; }
.channel-keys .keys-chip { cursor: pointer; transition: all 0.15s ease; }
.channel-keys .keys-chip:hover { background: rgba(var(--v-theme-primary), 0.06); border-color: rgba(var(--v-theme-primary), 0.3); color: rgb(var(--v-theme-primary)); }

.channel-actions { display: flex; align-items: center; gap: 2px; justify-content: flex-end; min-width: 50px; }

/* 备用资源池 */
.inactive-pool-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.inactive-pool {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 10px;
  background: rgba(var(--v-theme-surface-variant), 0.45);
  padding: 16px;
  border: 1px dashed rgba(var(--v-theme-outline), 0.65);
  border-radius: var(--cut-md) var(--cut-xs) var(--cut-md) var(--cut-xs);
}
.inactive-channel-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 10px 14px;
  background: rgb(var(--v-theme-surface));
  border: 1px solid rgba(var(--v-theme-outline), 0.55);
  border-radius: 7px 2px 7px 2px;
  transition: transform 0.15s var(--ease-out), border-color 0.15s ease, box-shadow 0.15s ease;
}
.inactive-channel-row:hover {
  border-color: rgb(var(--v-theme-primary));
  box-shadow: var(--shadow-1);
  transform: translateX(3px);
}
.inactive-channel-row .channel-info { flex: 1; min-width: 0; overflow: hidden; display: flex; flex-direction: column; gap: 2px; }
.inactive-channel-row .channel-info-main { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.inactive-channel-row .channel-info-desc { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; line-height: 1.3; max-width: 100%; font-size: 0.78rem; opacity: 0.5; }
.inactive-channel-row .channel-actions { display: flex; align-items: center; gap: 4px; }

/* tooltip */
.metrics-tooltip { font-size: 12px; line-height: 1.5; color: rgb(var(--v-theme-on-surface)); }
.metrics-tooltip-row { display: flex; justify-content: space-between; gap: 16px; padding: 2px 0; }
.metrics-tooltip-row span:first-child { color: rgba(var(--v-theme-on-surface), 0.55); }
.metrics-tooltip-row span:last-child { font-weight: 500; color: rgb(var(--v-theme-on-surface)); }
.model-preview-chip { max-width: 260px; overflow: hidden; }
.model-preview-chip :deep(.v-chip__content) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 日志对话框 */
.channel-logs-dialog-card { max-height: calc(100vh - 48px); display: flex; flex-direction: column; }
.channel-logs-title { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 16px 20px; }
.channel-logs-title-main { min-width: 0; display: flex; align-items: center; gap: 10px; }
.channel-logs-title-text { min-width: 0; display: flex; flex-direction: column; gap: 2px; font-size: 16px; font-weight: 650; line-height: 1.25; }
.channel-logs-subtitle { color: rgba(var(--v-theme-on-surface), 0.45); font-size: 12px; font-weight: 500; }
.channel-logs-dialog-body { overflow: auto; padding: 16px 20px 20px; }

.log-trend-panel {
  border: 1px solid rgba(var(--v-theme-outline), 0.3); border-radius: 4px;
  padding: 14px 16px 8px; margin-bottom: 16px;
  background: rgba(var(--v-theme-surface-variant), 0.15);
}
.log-trend-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 6px; }

.channel-logs-table-shell {
  max-height: min(46vh, 520px); overflow: auto;
  border: 1px solid rgba(var(--v-theme-outline), 0.3); border-radius: 4px;
  background: rgb(var(--v-theme-surface));
}
.channel-logs-table { min-width: 1160px; }
.channel-logs-table :deep(table) { table-layout: fixed; width: 100%; }
.log-col-time { width: 172px; }
.log-col-status { width: 108px; }
.log-col-model { width: 150px; }
.log-col-token { width: 116px; }
.log-col-cache { width: 142px; }
.log-col-upstream { width: 210px; }
.log-col-key { width: 142px; }
.log-col-duration { width: 90px; }
.log-col-error { width: 230px; }

.channel-logs-table th, .channel-logs-table td { vertical-align: middle; white-space: nowrap; }
.channel-logs-table th {
  position: sticky; top: 0; z-index: 1;
  background: rgb(var(--v-theme-surface));
  color: rgba(var(--v-theme-on-surface), 0.5);
  font-size: 12px; font-weight: 600;
  border-bottom: 1px solid rgba(var(--v-theme-outline), 0.1);
}
.channel-logs-table td {
  height: 48px; color: rgba(var(--v-theme-on-surface), 0.8);
  font-size: 13px; border-bottom: 1px solid rgba(var(--v-theme-outline), 0.06);
}
.log-time-cell, .log-key-cell, .log-duration-cell, .log-number-cell, .log-base-url { font-variant-numeric: tabular-nums; }
.log-key-cell, .log-duration-cell, .log-number-cell, .log-base-url, .log-model { font-family: 'Fira Code', 'JetBrains Mono', 'SF Mono', monospace; }
.log-model, .log-base-url { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.log-error { min-width: 0; }
.log-error-type { overflow: hidden; color: rgb(var(--v-theme-error)); font-size: 12px; font-weight: 700; text-overflow: ellipsis; white-space: nowrap; }
.log-error-message { display: -webkit-box; overflow: hidden; color: rgba(var(--v-theme-on-surface), 0.5); font-size: 12px; line-height: 1.35; white-space: normal; overflow-wrap: break-word; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.log-cell-note { color: rgba(var(--v-theme-on-surface), 0.45); font-size: 11px; line-height: 1.3; }

/* 响应式 */
@media (max-width: 1400px) {
  .channel-row-content { grid-template-columns: 28px 28px 85px minmax(100px, 1fr) auto 50px 50px 50px auto; gap: 6px; }
  .channel-row { padding: 10px 12px; }
}
@media (max-width: 1200px) {
  .channel-row-content { grid-template-columns: 26px 26px 80px minmax(80px, 1fr) auto 45px 45px 45px auto; gap: 5px; }
  .channel-row { padding: 8px 10px; }
  .rpm-tpm-values { font-size: 11px; }
  .rpm-tpm-labels { font-size: 8px; }
}
@media (max-width: 960px) {
  .channel-row-content { grid-template-columns: 26px 26px 75px minmax(60px, 1fr) auto 40px 40px 40px auto; gap: 4px; }
  .channel-row { padding: 8px 8px; }
}
@media (max-width: 600px) {
  .channel-row-content { grid-template-columns: 28px 1fr 60px; gap: 8px; }
  .channel-row { padding: 10px 12px; }
  .channel-metrics, .channel-latency, .channel-keys, .channel-rpm-tpm { display: none; }
  .priority-number, .drag-handle { display: none; }
}

@media (prefers-reduced-motion: reduce) {
  .channel-row,
  .channel-row::before,
  .channel-row::after,
  .drag-handle,
  .activity-bar,
  .activity-bar-warn,
  .activity-bar-bad,
  .activity-bar-latest,
  .mmb-fill,
  .mmb-fill::before,
  .mmb-fill::after,
  .mmb-flash,
  .mmb-value,
  .inactive-channel-row { transition: none !important; animation: none !important; }
}
</style>
