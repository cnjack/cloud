import { ArrowLeft } from '@phosphor-icons/react';
import jsQR from 'jsqr';
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { Button } from '@jcloud/device-ui';
import { useMobileAuth } from '../auth';
import { claimPairingOffer, parsePairingQr } from '../pairingOffer';

type CameraState = 'starting' | 'live' | 'unavailable';

/**
 * ScanPage — scan-to-pair (M11 W3). The webview camera (getUserMedia) feeds a
 * jsQR decode loop; on a `jcode://pair?…` payload the page claims the offer
 * (see pairingOffer.ts) and hands off to the device welcome page, where the
 * ordinary pairing poll/unwrap finishes the CEK exchange.
 *
 * Fallback: the QR payload can be pasted manually — emulators have no usable
 * camera and a denied permission lands here too.
 */
export function ScanPage() {
  const { t } = useTranslation();
  const auth = useMobileAuth();
  const navigate = useNavigate();
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [camera, setCamera] = useState<CameraState>('starting');
  const [manual, setManual] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const claimedRef = useRef(false);

  const claim = useCallback(
    async (raw: string) => {
      if (claimedRef.current || busy) return;
      if (!parsePairingQr(raw)) {
        setError(t('mobile.scan.invalid_qr'));
        return;
      }
      claimedRef.current = true;
      setBusy(true);
      setError(null);
      try {
        const result = await claimPairingOffer(raw, auth.token);
        if (!result.ok) {
          claimedRef.current = false;
          setError(t(`mobile.scan.${result.reason}`, { message: result.message ?? '?' }));
          return;
        }
        navigate(`/devices/${result.deviceId}`);
      } finally {
        setBusy(false);
      }
    },
    [auth.token, busy, navigate, t],
  );

  // Camera loop: stream → canvas → jsQR, stopped on unmount or first claim.
  useEffect(() => {
    let stream: MediaStream | null = null;
    let raf = 0;
    let cancelled = false;
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d', { willReadFrequently: true });

    const tick = () => {
      const video = videoRef.current;
      if (cancelled || !video || !ctx || claimedRef.current) return;
      if (video.readyState === video.HAVE_ENOUGH_DATA) {
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        ctx.drawImage(video, 0, 0);
        const frame = ctx.getImageData(0, 0, canvas.width, canvas.height);
        const hit = jsQR(frame.data, frame.width, frame.height);
        if (hit?.data) {
          void claim(hit.data);
          return;
        }
      }
      raf = requestAnimationFrame(tick);
    };

    void (async () => {
      try {
        stream = await navigator.mediaDevices.getUserMedia({
          video: { facingMode: 'environment' },
          audio: false,
        });
        if (cancelled) {
          stream.getTracks().forEach((track) => track.stop());
          return;
        }
        const video = videoRef.current;
        if (!video) return;
        video.srcObject = stream;
        await video.play();
        setCamera('live');
        raf = requestAnimationFrame(tick);
      } catch {
        if (!cancelled) setCamera('unavailable');
      }
    })();

    return () => {
      cancelled = true;
      cancelAnimationFrame(raf);
      stream?.getTracks().forEach((track) => track.stop());
    };
  }, [claim]);

  const submitManual = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const raw = manual.trim();
    if (raw) void claim(raw);
  };

  return (
    <div className="app-shell">
      <header className="topbar">
        <Link to="/" className="topbar-back" aria-label={t('device.list.title')}>
          <ArrowLeft size={18} />
        </Link>
        <div className="topbar-title">{t('mobile.scan.title')}</div>
      </header>

      <div className="content content-pad-bottom" data-testid="scan-page">
        <p className="state-block">{t('mobile.scan.hint')}</p>

        {camera !== 'unavailable' && (
          <div className="scan-viewfinder" data-testid="scan-camera">
            {/* playsInline + muted: iOS webview autoplay rules */}
            <video ref={videoRef} playsInline muted aria-label={t('mobile.scan.title')} />
          </div>
        )}
        {camera === 'unavailable' && (
          <p className="state-block" data-testid="scan-camera-unavailable">
            {t('mobile.scan.cameraUnavailable')}
          </p>
        )}

        {error && <p className="form-error" role="alert" data-testid="scan-error">{error}</p>}

        <form className="scan-manual" onSubmit={submitManual}>
          <label className="field">
            <span className="field-label">{t('mobile.scan.manualLabel')}</span>
            <textarea
              className="text-input"
              rows={3}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={manual}
              onChange={(e) => setManual(e.target.value)}
              placeholder="jcode://pair?cloud=…&device=…&offer=…&secret=…"
              data-testid="scan-manual-input"
            />
            <span className="field-hint">{t('mobile.scan.manualHint')}</span>
          </label>
          <Button type="submit" variant="primary" disabled={!manual.trim()} loading={busy}>
            {busy ? t('mobile.scan.claiming') : t('mobile.scan.submit')}
          </Button>
        </form>
      </div>
    </div>
  );
}
