/*
 * devDrive.ts — DEV-ONLY remote-eval hook for the M6 verification driver
 * (scripts/drive-ios.mjs). The vite dev server rebroadcasts m6:eval custom
 * HMR events; this module evaluates the expression (async-aware) and sends
 * back m6:result. Excluded from production builds by the import.meta.env.DEV
 * guard in main.tsx.
 */

interface EvalRequest {
  id: number;
  expr: string;
}

export function installDevDrive(): void {
  if (!import.meta.hot) return;
  import.meta.hot.on('m6:eval', (req: EvalRequest) => {
    void (async () => {
      let value: unknown = null;
      let error: string | null = null;
      try {
        // Async-iife wrapper gives the driver awaitPromise semantics.
        const fn = new Function(`return (async () => (${req.expr}))()`);
        value = await fn();
      } catch (err) {
        error = String(err);
      }
      import.meta.hot!.send('m6:result', { id: req.id, value, error });
    })();
  });
}
