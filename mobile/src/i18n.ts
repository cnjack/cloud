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
    session: {
      sendFailed: 'Could not send: {message}',
      deviceOffline: 'Device is offline',
      streamError: 'Live stream interrupted — reconnecting…',
      reconnect: 'Reconnect',
      emptyHistory: 'No events in this session yet.',
    },
    scan: {
      entry: 'Scan to pair',
      title: 'Scan to pair',
      hint: 'Point the camera at the pairing QR code shown by your desktop jcode.',
      cameraUnavailable: 'Camera unavailable — paste the QR content below instead.',
      invalid_qr: 'Not a jcode pairing QR code.',
      invalid_cloud: 'The QR code points at an invalid cloud URL.',
      unauthorized: 'Your session is not valid on this cloud — sign in again.',
      secret_mismatch: 'Pairing secret mismatch — rescan the QR code.',
      expired: 'This QR code has expired — show a fresh one on the desktop.',
      claimed: 'This QR code was already used — show a fresh one on the desktop.',
      unreachable: 'Could not reach the cloud.',
      failed: 'Pairing failed: {message}',
      manualLabel: 'Or paste the QR content',
      manualHint: 'The full jcode://pair?… string the QR code encodes.',
      submit: 'Pair device',
      claiming: 'Pairing…',
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
    session: {
      sendFailed: '发送失败：{message}',
      deviceOffline: '设备已离线',
      streamError: '实时流中断——正在重连…',
      reconnect: '重新连接',
      emptyHistory: '该会话还没有事件。',
    },
    scan: {
      entry: '扫码配对',
      title: '扫码配对',
      hint: '将摄像头对准桌面 jcode 显示的配对二维码。',
      cameraUnavailable: '摄像头不可用——请改为在下方粘贴二维码内容。',
      invalid_qr: '不是 jcode 配对二维码。',
      invalid_cloud: '二维码中的云端地址无效。',
      unauthorized: '当前会话在该云端无效——请重新登录。',
      secret_mismatch: '配对密钥不匹配——请重新扫码。',
      expired: '二维码已过期——请在桌面端生成新的二维码。',
      claimed: '二维码已被使用——请在桌面端生成新的二维码。',
      unreachable: '无法连接该云端。',
      failed: '配对失败：{message}',
      manualLabel: '或粘贴二维码内容',
      manualHint: '二维码编码的完整 jcode://pair?… 字符串。',
      submit: '配对设备',
      claiming: '配对中…',
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
    session: {
      sendFailed: '傳送失敗：{message}',
      deviceOffline: '裝置已離線',
      streamError: '即時串流中斷——重新連線中…',
      reconnect: '重新連線',
      emptyHistory: '此工作階段尚無事件。',
    },
    scan: {
      entry: '掃碼配對',
      title: '掃碼配對',
      hint: '將相機對準桌面 jcode 顯示的配對 QR Code。',
      cameraUnavailable: '相機不可用——請改為在下方貼上 QR Code 內容。',
      invalid_qr: '不是 jcode 配對 QR Code。',
      invalid_cloud: 'QR Code 中的雲端網址無效。',
      unauthorized: '目前工作階段在該雲端無效——請重新登入。',
      secret_mismatch: '配對金鑰不符——請重新掃碼。',
      expired: 'QR Code 已過期——請在桌面端產生新的 QR Code。',
      claimed: 'QR Code 已被使用——請在桌面端產生新的 QR Code。',
      unreachable: '無法連線到該雲端。',
      failed: '配對失敗：{message}',
      manualLabel: '或貼上 QR Code 內容',
      manualHint: 'QR Code 編碼的完整 jcode://pair?… 字串。',
      submit: '配對裝置',
      claiming: '配對中…',
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
    session: {
      sendFailed: '送信できませんでした: {message}',
      deviceOffline: 'デバイスはオフラインです',
      streamError: 'ライブストリームが中断されました — 再接続中…',
      reconnect: '再接続',
      emptyHistory: 'このセッションにはまだイベントがありません。',
    },
    scan: {
      entry: 'スキャンでペアリング',
      title: 'スキャンでペアリング',
      hint: 'デスクトップの jcode に表示されたペアリング QR コードをカメラに映してください。',
      cameraUnavailable: 'カメラを使用できません — 下に QR の内容を貼り付けてください。',
      invalid_qr: 'jcode のペアリング QR コードではありません。',
      invalid_cloud: 'QR コードのクラウド URL が無効です。',
      unauthorized: 'このクラウドでセッションが無効です — 再度サインインしてください。',
      secret_mismatch: 'ペアリングシークレットが一致しません — スキャンし直してください。',
      expired: 'この QR コードは期限切れです — デスクトップで新しいものを表示してください。',
      claimed: 'この QR コードは使用済みです — デスクトップで新しいものを表示してください。',
      unreachable: 'クラウドに接続できませんでした。',
      failed: 'ペアリングに失敗しました: {message}',
      manualLabel: 'または QR の内容を貼り付け',
      manualHint: 'QR コードがエンコードする完全な jcode://pair?… 文字列。',
      submit: 'デバイスをペアリング',
      claiming: 'ペアリング中…',
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
    session: {
      sendFailed: '전송할 수 없습니다: {message}',
      deviceOffline: '디바이스가 오프라인입니다',
      streamError: '라이브 스트림 중단 — 재연결 중…',
      reconnect: '재연결',
      emptyHistory: '이 세션에는 아직 이벤트가 없습니다.',
    },
    scan: {
      entry: '스캔으로 페어링',
      title: '스캔으로 페어링',
      hint: '데스크톱 jcode에 표시된 페어링 QR 코드를 카메라에 비춰주세요.',
      cameraUnavailable: '카메라를 사용할 수 없습니다 — 아래에 QR 내용을 붙여넣으세요.',
      invalid_qr: 'jcode 페어링 QR 코드가 아닙니다.',
      invalid_cloud: 'QR 코드의 클라우드 URL이 올바르지 않습니다.',
      unauthorized: '이 클라우드에서 세션이 유효하지 않습니다 — 다시 로그인하세요.',
      secret_mismatch: '페어링 시크릿이 일치하지 않습니다 — 다시 스캔하세요.',
      expired: 'QR 코드가 만료되었습니다 — 데스크톱에서 새 코드를 표시하세요.',
      claimed: '이미 사용된 QR 코드입니다 — 데스크톱에서 새 코드를 표시하세요.',
      unreachable: '클우드에 연결할 수 없습니다.',
      failed: '페어링 실패: {message}',
      manualLabel: '또는 QR 내용 붙여넣기',
      manualHint: 'QR 코드가 인코딩하는 전체 jcode://pair?… 문자열.',
      submit: '디바이스 페어링',
      claiming: '페어링 중…',
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
