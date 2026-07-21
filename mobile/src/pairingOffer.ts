/*
 * pairingOffer.ts — the scan-to-pair client half (M11 W3).
 *
 * The desktop jcode shows a QR encoding
 * `jcode://pair?cloud=..&device=..&offer=..&secret=..` (single-use offer,
 * 10min expiry). Scanning it here means: mint a P-256 pairing key pair, claim
 * the offer (POST /api/v1/pairing-offers/{offer}/claim), persist the
 * in-flight pairing session — from there the EXISTING useDevicePairing state
 * machine takes over unchanged (resume → poll → unwrap CEK → store), so the
 * wrap/unwrap and CEK storage logic is not duplicated.
 */
import { generatePairingKeys, sharedPairingSessions } from '@jcloud/device-ui';
import { validateCloudUrl } from './auth';

export interface PairingQr {
  cloud: string;
  device: string;
  offer: string;
  secret: string;
}

/** parsePairingQr validates a scanned/pasted QR payload. */
export function parsePairingQr(raw: string): PairingQr | null {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return null;
  }
  if (u.protocol !== 'jcode:' || u.hostname !== 'pair') return null;
  const cloud = u.searchParams.get('cloud');
  const device = u.searchParams.get('device');
  const offer = u.searchParams.get('offer');
  const secret = u.searchParams.get('secret');
  if (!cloud || !device || !offer || !secret) return null;
  return { cloud, device, offer, secret };
}

export type ClaimResult =
  | { ok: true; deviceId: string }
  | {
      ok: false;
      reason:
        | 'invalid_qr'
        | 'invalid_cloud'
        | 'unauthorized'
        | 'secret_mismatch'
        | 'expired'
        | 'claimed'
        | 'unreachable'
        | 'failed';
      message?: string;
    };

/**
 * claimPairingOffer turns a scanned QR into an in-flight CEK pairing and
 * returns the device id to navigate to. The Bearer token must belong to the
 * cloud account that owns the device.
 */
export async function claimPairingOffer(raw: string, token: string): Promise<ClaimResult> {
  const qr = parsePairingQr(raw);
  if (!qr) return { ok: false, reason: 'invalid_qr' };
  const cloud = validateCloudUrl(qr.cloud);
  if (!cloud.ok) return { ok: false, reason: 'invalid_cloud' };

  const keys = await generatePairingKeys();
  let res: Response;
  try {
    res = await fetch(`${cloud.url}/api/v1/pairing-offers/${encodeURIComponent(qr.offer)}/claim`, {
      method: 'POST',
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({
        secret: qr.secret,
        label: 'jcode-mobile',
        kty: 'P-256',
        pubkey: keys.pubkeyBase64,
      }),
    });
  } catch (err) {
    return { ok: false, reason: 'unreachable', message: String(err) };
  }
  if (res.status === 401) return { ok: false, reason: 'unauthorized' };
  if (res.status === 403) return { ok: false, reason: 'secret_mismatch' };
  if (res.status === 410) return { ok: false, reason: 'expired' };
  if (res.status === 409) return { ok: false, reason: 'claimed' };
  if (res.status !== 201) {
    return { ok: false, reason: 'failed', message: `HTTP ${res.status}` };
  }
  const body = (await res.json()) as { pairing_id?: string; device_id?: string };
  if (!body.pairing_id || !body.device_id) {
    return { ok: false, reason: 'failed', message: 'unexpected claim response' };
  }

  // Persist the in-flight pairing EXACTLY as useDevicePairing.start() would;
  // the device welcome page then resumes polling/unwrapping it.
  await sharedPairingSessions.put({
    deviceId: body.device_id,
    pairingId: body.pairing_id,
    pubkey: keys.pubkeyBase64,
    privateKeyJwk: keys.privateKeyJwk,
    createdAt: Date.now(),
  });
  return { ok: true, deviceId: body.device_id };
}
