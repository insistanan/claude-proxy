import { createVuetify } from 'vuetify'
import { h } from 'vue'
import type { IconSet, IconProps, ThemeDefinition } from 'vuetify'
import * as components from 'vuetify/components'
import * as directives from 'vuetify/directives'

// 引入样式
import 'vuetify/styles'

// 从 @mdi/js 按需导入使用的图标 (SVG)
import {
  mdiAccount,
  mdiSwapVerticalBold,
  mdiPlayCircle,
  mdiDragVertical,
  mdiOpenInNew,
  mdiKey,
  mdiRefresh,
  mdiDotsVertical,
  mdiPencil,
  mdiSpeedometer,
  mdiSpeedometerSlow,
  mdiRocketLaunch,
  mdiPauseCircle,
  mdiStopCircle,
  mdiDelete,
  mdiPlaylistRemove,
  mdiArchiveOutline,
  mdiPlus,
  mdiCheckCircle,
  mdiAlertCircle,
  mdiHelpCircle,
  mdiCloseCircle,
  mdiTag,
  mdiInformation,
  mdiCog,
  mdiWeb,
  mdiShieldAlert,
  mdiText,
  mdiSwapHorizontal,
  mdiArrowRight,
  mdiClose,
  mdiArrowUpBold,
  mdiArrowDownBold,
  mdiCheck,
  mdiContentCopy,
  mdiContentSave,
  mdiAlert,
  mdiWeatherNight,
  mdiWhiteBalanceSunny,
  mdiLogout,
  mdiServerNetwork,
  mdiHeartPulse,
  mdiChevronDown,
  mdiChevronUp,
  mdiChevronLeft,
  mdiChevronRight,
  mdiTune,
  mdiRotateRight,
  mdiDice6,
  mdiBackupRestore,
  mdiKeyPlus,
  mdiPin,
  mdiPinOutline,
  mdiKeyChain,
  mdiRobot,
  mdiRobotOutline,
  mdiMessageProcessing,
  mdiMessageReplyText,
  mdiDiamondStone,
  mdiApi,
  mdiLightningBolt,
  mdiFormTextbox,
  mdiMenuDown,
  mdiMenuUp,
  mdiCheckboxMarked,
  mdiCheckboxBlankOutline,
  mdiMinusBox,
  mdiCircle,
  mdiRadioboxMarked,
  mdiRadioboxBlank,
  mdiStar,
  mdiStarOutline,
  mdiStarHalf,
  mdiPageFirst,
  mdiPageLast,
  mdiUnfoldMoreHorizontal,
  mdiLoading,
  mdiClockOutline,
  mdiChartLineVariant,
  mdiMagnify,
  mdiCalendar,
  mdiPaperclip,
  mdiEyedropper,
  mdiShieldRefresh,
  mdiShieldOffOutline,
  mdiAlertCircleOutline,
  mdiChartTimelineVariant,
  mdiChartAreaspline,
  mdiChartLine,
  mdiCodeBraces,
  mdiDatabase,
  mdiTextBoxSearchOutline,
  mdiSignature,
  mdiImageSearchOutline,
  mdiChatOutline,
  mdiChatProcessing,
  mdiSend,
  mdiArrowCollapseUp,
  mdiArrowCollapseDown,
  mdiArchiveClockOutline,
  mdiFormatListBulleted,
  mdiSort,
  mdiTimerSand,
  mdiTestTube,
  mdiImage,
  mdiTranslate,
} from '@mdi/js'

// 图标名称到 SVG path 的映射 (使用 kebab-case)
const iconMap: Record<string, string> = {
  'complete': mdiCheck,
  'cancel': mdiCloseCircle,
  'close': mdiClose,
  'delete': mdiDelete,
  'clear': mdiClose,
  'success': mdiCheckCircle,
  'info': mdiInformation,
  'warning': mdiAlert,
  'error': mdiAlertCircle,
  'prev': mdiChevronLeft,
  'next': mdiChevronRight,
  'checkboxOn': mdiCheckboxMarked,
  'checkboxOff': mdiCheckboxBlankOutline,
  'checkboxIndeterminate': mdiMinusBox,
  'delimiter': mdiCircle,
  'sortAsc': mdiArrowUpBold,
  'sortDesc': mdiArrowDownBold,
  'expand': mdiChevronDown,
  'menu': mdiMenuDown,
  'subgroup': mdiMenuDown,
  'dropdown': mdiMenuDown,
  'radioOn': mdiRadioboxMarked,
  'radioOff': mdiRadioboxBlank,
  'edit': mdiPencil,
  'ratingEmpty': mdiStarOutline,
  'ratingFull': mdiStar,
  'ratingHalf': mdiStarHalf,
  'loading': mdiLoading,
  'first': mdiPageFirst,
  'last': mdiPageLast,
  'unfold': mdiUnfoldMoreHorizontal,
  'file': mdiPaperclip,
  'magnify': mdiMagnify,
  'plus': mdiPlus,
  'minus': mdiMinusBox,
  'calendar': mdiCalendar,
  'treeviewCollapse': mdiMenuDown,
  'treeviewExpand': mdiMenuUp,
  'eyeDropper': mdiEyedropper,

  // 布局与导航
  'swap-vertical-bold': mdiSwapVerticalBold,
  'drag-vertical': mdiDragVertical,
  'open-in-new': mdiOpenInNew,
  'chevron-down': mdiChevronDown,
  'chevron-up': mdiChevronUp,
  'chevron-left': mdiChevronLeft,
  'chevron-right': mdiChevronRight,
  'dots-vertical': mdiDotsVertical,
  'logout': mdiLogout,
  'archive-outline': mdiArchiveOutline,
  'archive-clock-outline': mdiArchiveClockOutline,
  'content-save': mdiContentSave,
  'menu-down': mdiMenuDown,
  'menu-up': mdiMenuUp,

  // 操作按钮
  'pencil': mdiPencil,
  'refresh': mdiRefresh,
  'check': mdiCheck,
  'content-copy': mdiContentCopy,
  'arrow-up-bold': mdiArrowUpBold,
  'arrow-down-bold': mdiArrowDownBold,
  'arrow-right': mdiArrowRight,
  'swap-horizontal': mdiSwapHorizontal,
  'rotate-right': mdiRotateRight,
  'backup-restore': mdiBackupRestore,

  // 状态图标
  'play-circle': mdiPlayCircle,
  'pause-circle': mdiPauseCircle,
  'stop-circle': mdiStopCircle,
  'check-circle': mdiCheckCircle,
  'alert-circle': mdiAlertCircle,
  'alert-circle-outline': mdiAlertCircleOutline,
  'close-circle': mdiCloseCircle,
  'help-circle': mdiHelpCircle,
  'alert': mdiAlert,

  'shield-refresh': mdiShieldRefresh,
  'shield-off-outline': mdiShieldOffOutline,

  'key': mdiKey,
  'key-plus': mdiKeyPlus,
  'key-chain': mdiKeyChain,
  'speedometer': mdiSpeedometer,
  'speedometer-slow': mdiSpeedometerSlow,
  'rocket-launch': mdiRocketLaunch,
  'playlist-remove': mdiPlaylistRemove,
  'tag': mdiTag,
  'information': mdiInformation,
  'cog': mdiCog,
  'web': mdiWeb,
  'shield-alert': mdiShieldAlert,
  'text': mdiText,
  'tune': mdiTune,
  'dice-6': mdiDice6,
  'heart-pulse': mdiHeartPulse,
  'server-network': mdiServerNetwork,
  'pin': mdiPin,
  'pin-outline': mdiPinOutline,
  'lightning-bolt': mdiLightningBolt,
  'form-textbox': mdiFormTextbox,
  'clock-outline': mdiClockOutline,
  'chart-line-variant': mdiChartLineVariant,
  'paperclip': mdiPaperclip,
  'eye-dropper': mdiEyedropper,

  // 主题切换
  'weather-night': mdiWeatherNight,
  'white-balance-sunny': mdiWhiteBalanceSunny,

  // 服务类型图标
  'robot': mdiRobot,
  'robot-outline': mdiRobotOutline,
  'message-processing': mdiMessageProcessing,
  'message-reply-text': mdiMessageReplyText,
  'chat-outline': mdiChatOutline,
  'chat-processing': mdiChatProcessing,
  'diamond-stone': mdiDiamondStone,
  'api': mdiApi,
  'image': mdiImage,
  'translate': mdiTranslate,

  'checkbox-marked': mdiCheckboxMarked,
  'checkbox-blank-outline': mdiCheckboxBlankOutline,
  'minus-box': mdiMinusBox,
  'radiobox-marked': mdiRadioboxMarked,
  'radiobox-blank': mdiRadioboxBlank,
  'star': mdiStar,
  'star-outline': mdiStarOutline,
  'star-half': mdiStarHalf,
  'page-first': mdiPageFirst,
  'page-last': mdiPageLast,
  'unfold-more-horizontal': mdiUnfoldMoreHorizontal,
  'circle': mdiCircle,
  'chart-timeline-variant': mdiChartTimelineVariant,
  'chart-areaspline': mdiChartAreaspline,
  'chart-line': mdiChartLine,
  'code-braces': mdiCodeBraces,
  'database': mdiDatabase,
  'text-box-search-outline': mdiTextBoxSearchOutline,
  'signature': mdiSignature,
  'image-search-outline': mdiImageSearchOutline,
  'arrow-collapse-up': mdiArrowCollapseUp,
  'arrow-collapse-down': mdiArrowCollapseDown,
  'format-list-bulleted': mdiFormatListBulleted,
  'sort': mdiSort,
  'timer-sand': mdiTimerSand,
  'test-tube': mdiTestTube,
  'account': mdiAccount,
  'send': mdiSend,
}

// 自定义 SVG iconset
const customSvgIconSet: IconSet = {
  component: (props: IconProps) => {
    let iconName = props.icon as string
    if (iconName.startsWith('mdi-')) {
      iconName = iconName.substring(4)
    }
    const svgPath = iconMap[iconName]

    if (!svgPath) {
      if (import.meta.env.DEV) {
        console.warn(`[Vuetify Icon] 未找到图标: ${iconName}`)
      }
      return h('svg', {
        class: 'v-icon__svg v-icon__svg--missing',
        xmlns: 'http://www.w3.org/2000/svg',
        viewBox: '0 0 24 24',
        role: 'img',
        'aria-hidden': 'true',
        style: { fontSize: 'inherit', width: '1em', height: '1em' },
      }, [h('path', { d: mdiHelpCircle, fill: 'currentColor' })])
    }

    return h('svg', {
      class: 'v-icon__svg',
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      role: 'img',
      'aria-hidden': 'true',
      style: { fontSize: 'inherit', width: '1em', height: '1em' },
    }, [h('path', { d: svgPath, fill: 'currentColor' })])
  }
}

// ============================================================
// 🎨 色彩系统 — 暖色调高级感设计
// ============================================================

// Light Theme — 暖白色调和丰富层次
const lightTheme: ThemeDefinition = {
  dark: false,
  colors: {
    // 主色调 — 深邃蓝
    primary: '#1D4ED8',
    'primary-darken-1': '#1E40AF',
    'primary-lighten-1': '#3B82F6',
    secondary: '#7C3AED',
    accent: '#D97706',

    // 语义色彩 — 高饱和度，更醒目
    info: '#0891B2',
    success: '#059669',
    warning: '#D97706',
    error: '#DC2626',

    // 表面色 — 暖白层次
    background: '#F3F0EB',
    surface: '#FFFFFF',
    'surface-variant': '#F8F6F2',
    'surface-bright': '#FFFFFF',
    'on-surface': '#1C1917',
    'on-background': '#292524',
    'on-surface-variant': '#57534E',
    'outline': '#D6D3D1',
    'outline-variant': '#E7E5E4',
  }
}

// Dark Theme — 深邃高对比，同步优化
const darkTheme: ThemeDefinition = {
  dark: true,
  colors: {
    primary: '#60A5FA',
    'primary-darken-1': '#3B82F6',
    'primary-lighten-1': '#93C5FD',
    secondary: '#A78BFA',
    accent: '#FBBF24',

    info: '#38BDF8',
    success: '#34D399',
    warning: '#FBBF24',
    error: '#F87171',

    // 表面色 — 深色层次分明
    background: '#0F0D0A',
    surface: '#1C1917',
    'surface-variant': '#292524',
    'surface-bright': '#292524',
    'on-surface': '#F5F5F0',
    'on-background': '#E7E5E4',
    'on-surface-variant': '#A8A29E',
    'outline': '#44403C',
    'outline-variant': '#292524',
  }
}

export default createVuetify({
  components,
  directives,
  icons: {
    defaultSet: 'mdi',
    sets: { mdi: customSvgIconSet }
  },
  theme: {
    defaultTheme: 'light',
    themes: {
      light: lightTheme,
      dark: darkTheme
    },
    variations: {
      colors: ['primary', 'secondary', 'info', 'success', 'warning', 'error'],
      lighten: 3,
      darken: 2,
    }
  },
  defaults: {
    VCard: {
      elevation: 0,
      rounded: 'lg',
    },
    VBtn: {
      rounded: 'lg',
    },
    VChip: {
      rounded: 'md',
    },
    VDialog: {
      rounded: 'xl',
    },
    VMenu: {
      rounded: 'lg',
    },
    VTooltip: {
      location: 'top',
    },
    VTextField: {
      variant: 'outlined',
      density: 'comfortable',
    },
    VTextarea: {
      variant: 'outlined',
      density: 'comfortable',
    },
    VSelect: {
      variant: 'outlined',
      density: 'comfortable',
    },
    VCombobox: {
      variant: 'outlined',
      density: 'comfortable',
    },
    VAutocomplete: {
      variant: 'outlined',
      density: 'comfortable',
    },
    VTable: {
      density: 'comfortable',
    },
    VList: {
      density: 'comfortable',
    },
    VExpansionPanel: {
      variant: 'accordion',
    },
  }
})