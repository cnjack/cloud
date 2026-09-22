import { useEffect, useRef, type ReactNode, type KeyboardEvent } from 'react';
import { useAccountProfile } from '../api/accountProfile';
import { useOptionalAuth } from '../auth/AuthProvider';
import { setLocale } from '../i18n';
import { setTheme } from '../theme';

/** Apply account defaults at sign-in; later local choices remain under user control. */
export function AccountPreferencesBoundary({ children }: { children: ReactNode }) {
  const profile = useAccountProfile();
  const auth = useOptionalAuth();
  const applied = useRef('');
  const userID = auth?.me?.user.id;
  useEffect(() => {
    if (!userID || !profile.data || applied.current === userID) return;
    applied.current = userID;
    const { language, theme } = profile.data.preferences;
    if (language) void setLocale(language);
    if (theme) setTheme(theme);
  }, [profile.data, userID]);
  const onKeyDownCapture = (event: KeyboardEvent) => {
    const target = event.target;
    if (profile.data?.preferences.send_key !== 'mod_enter' || !(target instanceof HTMLTextAreaElement)
      || !target.closest('.jcode-product') || event.key !== 'Enter' || event.metaKey || event.ctrlKey
      || event.nativeEvent.isComposing || event.keyCode === 229) return;
    // Leave the browser's newline default intact, while preventing the shared
    // composer's Enter-to-send handler. Modified Enter continues through it.
    event.stopPropagation();
  };
  return <div style={{ display: 'contents' }} onKeyDownCapture={onKeyDownCapture}>{children}</div>;
}
