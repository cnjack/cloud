/*
 * i18n.ts — mobile shell i18next. Ships the @jcloud/device-ui bundles
 * (device.*, run.permission.*, five locales, generated from the console
 * locale files) plus the shell's own mobile.* keys. Same `{name}`
 * interpolation convention as the console.
 */
import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import {
  DEVICE_UI_LOCALES,
  deviceUiInterpolation,
  deviceUiResources,
} from '@jcloud/device-ui';

type ShellBundle = Record<string, unknown>;

const en: ShellBundle = {
  mobile: {
    login: {
      title: 'jcode Cloud',
      subtitle: 'Connect to your cloud and drive your jcode devices.',
      cloudUrl: 'Cloud URL',
      token: 'Session token',
      tokenHint:
        'A user session token (Bearer). The console service token cannot list devices.',
      submit: 'Sign in',
      signingIn: 'Signing in…',
      invalidUrl: 'Enter a valid URL, e.g. https://cloud.j-code.net',
      httpNotAllowed: 'Plain http is only allowed for localhost / 127.0.0.1 / 10.0.2.2 (use https)',
      unauthorized: 'Token rejected — sign in again.',
      unreachable: 'Could not reach the cloud at this URL.',
      failed: 'Sign-in failed: {message}',
      signInWithCloud: 'Sign in with cloud',
      oauthWaiting: 'Finish signing in in the browser — this app continues automatically.',
      oauthOpenFailed: 'Could not open the system browser.',
      manualFallback: 'or paste a session token',
    },
    common: {
      loading: 'Loading…',
      retry: 'Retry',
      logout: 'Sign out',
      untitled: 'Untitled session',
    },
    devices: {
      loadError: 'Could not load devices',
      emptyTitle: 'No devices yet',
      emptyBody: 'Run `jcode login` on a machine to connect it to jcode Cloud.',
      online: 'Online',
      offline: 'Offline',
      lastSeen: 'Last seen {time}',
      neverSeen: 'Never seen',
    },
    compose: {
      toggle: 'Session options',
    },
    session: {
      sendFailed: 'Could not send: {message}',
      deviceOffline: 'Device is offline',
      streamError: 'Live stream interrupted — reconnecting…',
      reconnect: 'Reconnect',
      emptyHistory: 'No events in this session yet.',
    },
  },
};

const zhHans: ShellBundle = {
  mobile: {
    login: {
      title: 'jcode Cloud',
      subtitle: '连接到你的云端，随时驱动你的 jcode 设备。',
      cloudUrl: '云端地址',
      token: '会话令牌',
      tokenHint: '用户会话令牌（Bearer）。console 服务令牌无法列出设备。',
      submit: '登录',
      signingIn: '登录中…',
      invalidUrl: '请输入合法 URL，例如 https://cloud.j-code.net',
      httpNotAllowed: '仅 localhost / 127.0.0.1 / 10.0.2.2 允许使用 http（请用 https）',
      unauthorized: '令牌无效，请重新登录。',
      unreachable: '无法连接该云端地址。',
      failed: '登录失败：{message}',
      signInWithCloud: '使用云端账号登录',
      oauthWaiting: '请在浏览器中完成登录——本应用会自动继续。',
      oauthOpenFailed: '无法打开系统浏览器。',
      manualFallback: '或手动粘贴会话令牌',
    },
    common: {
      loading: '加载中…',
      retry: '重试',
      logout: '退出登录',
      untitled: '未命名会话',
    },
    devices: {
      loadError: '设备加载失败',
      emptyTitle: '还没有设备',
      emptyBody: '在一台机器上运行 `jcode login` 把它接入 jcode Cloud。',
      online: '在线',
      offline: '离线',
      lastSeen: '最近在线 {time}',
      neverSeen: '从未在线',
    },
    compose: {
      toggle: '会话选项',
    },
    session: {
      sendFailed: '发送失败：{message}',
      deviceOffline: '设备已离线',
      streamError: '实时流中断——正在重连…',
      reconnect: '重新连接',
      emptyHistory: '该会话还没有事件。',
    },
  },
};

const zhHant: ShellBundle = {
  mobile: {
    login: {
      title: 'jcode Cloud',
      subtitle: '連線到你的雲端，隨時驅動你的 jcode 裝置。',
      cloudUrl: '雲端網址',
      token: '工作階段權杖',
      tokenHint: '使用者工作階段權杖（Bearer）。console 服務權杖無法列出裝置。',
      submit: '登入',
      signingIn: '登入中…',
      invalidUrl: '請輸入合法 URL，例如 https://cloud.j-code.net',
      httpNotAllowed: '僅 localhost / 127.0.0.1 / 10.0.2.2 允許使用 http（請用 https）',
      unauthorized: '權杖無效，請重新登入。',
      unreachable: '無法連線到該雲端網址。',
      failed: '登入失敗：{message}',
      signInWithCloud: '使用雲端帳號登入',
      oauthWaiting: '請在瀏覽器中完成登入——本應用會自動繼續。',
      oauthOpenFailed: '無法開啟系統瀏覽器。',
      manualFallback: '或手動貼上工作階段權杖',
    },
    common: {
      loading: '載入中…',
      retry: '重試',
      logout: '登出',
      untitled: '未命名工作階段',
    },
    devices: {
      loadError: '裝置載入失敗',
      emptyTitle: '尚無裝置',
      emptyBody: '在一台機器上執行 `jcode login` 把它接入 jcode Cloud。',
      online: '線上',
      offline: '離線',
      lastSeen: '最近上線 {time}',
      neverSeen: '從未上線',
    },
    compose: {
      toggle: '工作階段選項',
    },
    session: {
      sendFailed: '傳送失敗：{message}',
      deviceOffline: '裝置已離線',
      streamError: '即時串流中斷——重新連線中…',
      reconnect: '重新連線',
      emptyHistory: '此工作階段尚無事件。',
    },
  },
};

const ja: ShellBundle = {
  mobile: {
    login: {
      title: 'jcode Cloud',
      subtitle: 'クラウドに接続して、jcode デバイスを操作しましょう。',
      cloudUrl: 'クラウド URL',
      token: 'セッショントークン',
      tokenHint: 'ユーザーセッショントークン（Bearer）。console のサービストークンではデバイスを一覧できません。',
      submit: 'サインイン',
      signingIn: 'サインイン中…',
      invalidUrl: '有効な URL を入力してください（例: https://cloud.j-code.net）',
      httpNotAllowed: '平文 http は localhost / 127.0.0.1 / 10.0.2.2 のみ許可されます（https を使用）',
      unauthorized: 'トークンが拒否されました。再度サインインしてください。',
      unreachable: 'この URL のクラウドに接続できませんでした。',
      failed: 'サインインに失敗しました: {message}',
      signInWithCloud: 'クラウドでサインイン',
      oauthWaiting: 'ブラウザでサインインを完了してください — このアプリは自動で続行します。',
      oauthOpenFailed: 'システムブラウザを開けませんでした。',
      manualFallback: 'またはセッショントークンを貼り付け',
    },
    common: {
      loading: '読み込み中…',
      retry: '再試行',
      logout: 'サインアウト',
      untitled: '無題のセッション',
    },
    devices: {
      loadError: 'デバイスを読み込めませんでした',
      emptyTitle: 'デバイスがまだありません',
      emptyBody: 'マシンで `jcode login` を実行して jcode Cloud に接続してください。',
      online: 'オンライン',
      offline: 'オフライン',
      lastSeen: '最終確認 {time}',
      neverSeen: '未接続',
    },
    compose: {
      toggle: 'セッションオプション',
    },
    session: {
      sendFailed: '送信できませんでした: {message}',
      deviceOffline: 'デバイスはオフラインです',
      streamError: 'ライブストリームが中断されました — 再接続中…',
      reconnect: '再接続',
      emptyHistory: 'このセッションにはまだイベントがありません。',
    },
  },
};

const ko: ShellBundle = {
  mobile: {
    login: {
      title: 'jcode Cloud',
      subtitle: '클우드에 연결하고 jcode 디바이스를 구동하세요.',
      cloudUrl: '클우드 URL',
      token: '세션 토큰',
      tokenHint: '사용자 세션 토큰(Bearer). console 서비스 토큰으로는 디바이스를 조회할 수 없습니다.',
      submit: '로그인',
      signingIn: '로그인 중…',
      invalidUrl: '유효한 URL을 입력하세요 (예: https://cloud.j-code.net)',
      httpNotAllowed: '일반 http는 localhost / 127.0.0.1 / 10.0.2.2만 허용됩니다(https 사용)',
      unauthorized: '토큰이 거부되었습니다 — 다시 로그인하세요.',
      unreachable: '이 URL의 클라우드에 연결할 수 없습니다.',
      failed: '로그인 실패: {message}',
      signInWithCloud: '클우드로 로그인',
      oauthWaiting: '브라우저에서 로그인을 완료하세요 — 앱이 자동으로 이어집니다.',
      oauthOpenFailed: '시스템 브라우저를 열 수 없습니다.',
      manualFallback: '또는 세션 토큰 붙여넣기',
    },
    common: {
      loading: '불러오는 중…',
      retry: '다시 시도',
      logout: '로그아웃',
      untitled: '제목 없는 세션',
    },
    devices: {
      loadError: '디바이스를 불러올 수 없습니다',
      emptyTitle: '아직 디바이스가 없습니다',
      emptyBody: '머신에서 `jcode login`을 실행해 jcode Cloud에 연결하세요.',
      online: '온라인',
      offline: '오프라인',
      lastSeen: '마지막 확인 {time}',
      neverSeen: '확인된 적 없음',
    },
    compose: {
      toggle: '세션 옵션',
    },
    session: {
      sendFailed: '전송할 수 없습니다: {message}',
      deviceOffline: '디바이스가 오프라인입니다',
      streamError: '라이브 스트림 중단 — 재연결 중…',
      reconnect: '재연결',
      emptyHistory: '이 세션에는 아직 이벤트가 없습니다.',
    },
  },
};

const SHELL: Record<string, ShellBundle> = {
  en,
  'zh-Hans': zhHans,
  'zh-Hant': zhHant,
  ja,
  ko,
};

type Locale = (typeof DEVICE_UI_LOCALES)[number];
const FALLBACK: Locale = 'en';
const STORAGE_KEY = 'jcloud_locale';

function isSupported(value: string | null | undefined): value is Locale {
  return !!value && (DEVICE_UI_LOCALES as readonly string[]).includes(value);
}

function browserLocale(): Locale {
  if (typeof navigator === 'undefined') return FALLBACK;
  const tags = navigator.languages?.length ? navigator.languages : [navigator.language];
  for (const tag of tags) {
    const lower = tag.toLowerCase();
    if (lower === 'zh' || lower.startsWith('zh-cn') || lower.startsWith('zh-sg') || lower.startsWith('zh-hans')) return 'zh-Hans';
    if (lower.startsWith('zh-tw') || lower.startsWith('zh-hk') || lower.startsWith('zh-mo') || lower.startsWith('zh-hant')) return 'zh-Hant';
    const primary = lower.split('-')[0];
    if (primary === 'ja') return 'ja';
    if (primary === 'ko') return 'ko';
    if (primary === 'en') return 'en';
  }
  return FALLBACK;
}

function initialLocale(): Locale {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (isSupported(stored)) return stored;
  } catch {
    /* storage unavailable */
  }
  return browserLocale();
}

const resources = Object.fromEntries(
  DEVICE_UI_LOCALES.map((lng) => [
    lng,
    {
      translation: {
        ...deviceUiResources[lng].translation,
        ...SHELL[lng],
      },
    },
  ]),
);

void i18n.use(initReactI18next).init({
  resources,
  lng: initialLocale(),
  fallbackLng: FALLBACK,
  interpolation: { ...deviceUiInterpolation },
});

export { i18n };
