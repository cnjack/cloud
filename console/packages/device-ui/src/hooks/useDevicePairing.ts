/*
 * useDevicePairing — the client-side pairing state machine (docs/17 §6.3).
 *
 *   idle ──start()──▶ pending ──device approves──▶ ready (CEK stored)
 *                      │
 *                      ├──device denies──▶ denied ──start()──▶ pending
 *                      └──10min window────▶ expired ──start()──▶ pending
 *
 * `ready` means this client holds the device's CEK (IndexedDB via the
 * DeviceCrypto store) and the E2EE layer decrypts transparently. A pending
 * pairing polls GET /devices/{id}/pairings/{pid}; on approval it unwraps the
 * CEK with the locally-held P-256 private key, stores it, and invalidates the
 * device queries so encrypted content refetches into plaintext. The in-flight
 * pairing (id + private key JWK) is persisted so a reload resumes the poll.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useDeviceApi } from '../api/DeviceApiProvider';
import { dqk } from '../api/deviceQueries';
import type { DeviceApi } from '../api/devices';
import { generatePairingKeys, importPairingPrivateKey, unwrapCek } from '../devicecrypto/pairing';
import { sharedDeviceCrypto, sharedPairingSessions } from '../devicecrypto/provider';
import type { DeviceCrypto } from '../devicecrypto/provider';
import type { PairingSession, PairingSessionStore } from '../devicecrypto/storage';

export type DevicePairingPhase =
  /** Resolving local state (CEK / in-flight pairing lookup). */
  | 'loading'
  /** The CEK is in hand — E2EE is live. */
  | 'ready'
  /** No CEK, no pairing in flight — the guide card offers `start`. */
  | 'idle'
  /** Waiting for the on-device approval (`jcode cloud approve <id>`). */
  | 'pending'
  | 'denied'
  | 'expired'
  | 'error';

export interface DevicePairingDeps {
  api?: DeviceApi;
  crypto?: DeviceCrypto;
  sessions?: PairingSessionStore;
  /** Requester label shown on the device's approval prompt. */
  label?: string;
  /** Pending-state poll cadence (ms). */
  pollMs?: number;
}

export interface DevicePairing {
  phase: DevicePairingPhase;
  /** The in-flight pairing id (the device approves it by this id). */
  pairingId: string | null;
  /** Begin (or restart, after denied/expired) a pairing. */
  start: () => void;
  starting: boolean;
  error: unknown;
}

export function useDevicePairing(deviceId: string, deps: DevicePairingDeps = {}): DevicePairing {
  const defaultApi = useDeviceApi();
  const api = deps.api ?? defaultApi;
  const crypto = deps.crypto ?? sharedDeviceCrypto;
  const sessions = deps.sessions ?? sharedPairingSessions;
  const label = deps.label ?? 'console-web';
  const pollMs = deps.pollMs ?? 3000;
  const qc = useQueryClient();

  const [phase, setPhase] = useState<DevicePairingPhase>('loading');
  const [pairingId, setPairingId] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const sessionRef = useRef<PairingSession | null>(null);

  // Initial resolution: CEK in hand → ready; a persisted in-flight pairing
  // resumes as pending; otherwise the guide card (idle).
  useEffect(() => {
    if (!deviceId) return;
    let cancelled = false;
    setPhase('loading');
    setPairingId(null);
    setError(null);
    sessionRef.current = null;
    (async () => {
      try {
        if (await crypto.getKey(deviceId)) {
          if (!cancelled) setPhase('ready');
          return;
        }
        const session = await sessions.get(deviceId);
        if (cancelled) return;
        if (session) {
          sessionRef.current = session;
          setPairingId(session.pairingId);
          setPhase('pending');
        } else {
          setPhase('idle');
        }
      } catch (err) {
        if (!cancelled) {
          setError(err);
          setPhase('error');
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [api, crypto, sessions, deviceId]);

  const start = useCallback(() => {
    if (!deviceId || starting) return;
    setStarting(true);
    setError(null);
    (async () => {
      try {
        const keys = await generatePairingKeys();
        const res = await api.createPairing(deviceId, {
          label,
          kty: 'P-256',
          pubkey: keys.pubkeyBase64,
        });
        const session: PairingSession = {
          deviceId,
          pairingId: res.pairing_id,
          pubkey: keys.pubkeyBase64,
          privateKeyJwk: keys.privateKeyJwk,
          createdAt: Date.now(),
        };
        await sessions.put(session);
        sessionRef.current = session;
        setPairingId(res.pairing_id);
        setPhase('pending');
      } catch (err) {
        setError(err);
        setPhase('error');
      } finally {
        setStarting(false);
      }
    })();
  }, [api, crypto, sessions, deviceId, label, starting]);

  // While pending, poll the pairing state until it resolves.
  useEffect(() => {
    if (phase !== 'pending' || !deviceId || !pairingId) return;
    let cancelled = false;

    const poll = async () => {
      let state;
      try {
        state = await api.getPairing(deviceId, pairingId);
      } catch {
        return; // transient: keep polling
      }
      if (cancelled) return;
      switch (state.status) {
        case 'approved': {
          const session = sessionRef.current ?? (await sessions.get(deviceId));
          if (cancelled) return;
          if (!session || !state.wrap) {
            setError(new Error('pairing approved but the local key is gone — pair again'));
            setPhase('error');
            return;
          }
          try {
            const privateKey = await importPairingPrivateKey(session.privateKeyJwk);
            const { cek, keyGen } = await unwrapCek(privateKey, state.wrap);
            await crypto.store.put(deviceId, { cek, keyGen });
            await sessions.delete(deviceId);
            sessionRef.current = null;
            if (cancelled) return;
            setPairingId(null);
            setPhase('ready');
            // Refetch so already-cached ciphertext re-renders as plaintext.
            // The devices row must be invalidated too: it carries the sealed
            // capabilities envelope, and without a refetch the composer
            // pickers (models/projects/slash) stay empty until a full reload.
            qc.invalidateQueries({ queryKey: dqk.deviceSessions(deviceId) });
            qc.invalidateQueries({ queryKey: dqk.devices });
          } catch (err) {
            if (!cancelled) {
              setError(err);
              setPhase('error');
            }
          }
          return;
        }
        case 'denied':
        case 'expired': {
          await sessions.delete(deviceId);
          sessionRef.current = null;
          if (cancelled) return;
          setPairingId(null);
          setPhase(state.status);
          return;
        }
        default:
          return; // still pending
      }
    };

    void poll();
    const timer = setInterval(() => void poll(), pollMs);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [api, crypto, sessions, qc, deviceId, pairingId, phase, pollMs]);

  return { phase, pairingId, start, starting, error };
}
