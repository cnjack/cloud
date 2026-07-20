/*
 * types.ts — the permission-card view types shared by the device timeline and
 * the console's runview. The console's runview/types.ts re-exports these from
 * here so the PermissionCard implementation (this package) and the runview
 * event model agree on one shape.
 */

/** One option a permission request offered (F8b), the view shape. */
export interface PermissionOptionView {
  optionId: string;
  name: string;
  /** jcode's option classification (e.g. "allow_once" / "reject_once"). */
  kind: string;
}

/**
 * An `agent.permission_request` paired with its `agent.permission_resolved`
 * by request_id (F8b). status 'pending' until the resolved event lands; the
 * user's option buttons live on the pending card, the outcome badge on the
 * resolved one.
 */
export interface PermissionCardItem {
  kind: 'permission_card';
  /** Anchor seq = the request's seq (stable React key). */
  seq: number;
  /** ts of the request. */
  ts: string;
  requestId: string;
  toolCallId?: string;
  title: string;
  options: PermissionOptionView[];
  status: 'pending' | 'resolved';
  /** The option that actually took effect (resolved only; '' = none/cancelled). */
  resolvedOptionId?: string;
  /** "user" (a person answered) or "timeout" (the runner deny-safed it). */
  resolution?: string;
  resolvedSeq?: number;
}

/**
 * Host-injected controls for the pending PermissionCard's option buttons
 * (F8b). The card never talks to an API itself — the host supplies the decide
 * callback plus the optimistic/read-only state.
 */
export interface PermissionControls {
  /** Called when the user picks an option on a pending card. */
  onDecide?: (requestId: string, optionId: string) => void;
  /** True renders every option button disabled (read-only viewer role). */
  disabled?: boolean;
  /**
   * requestId → optionId already submitted (optimistic): the card greys its
   * buttons and marks the chosen one while the resolved event is awaited.
   */
  decided?: Record<string, string>;
}
