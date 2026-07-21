/*
 * runtime.ts — a ChatRuntime (jcode-ui-core) over the device relay.
 *
 * The product composer (ChatInput) and Thread read timeline/queue/running
 * state and dispatch send/stop/approval actions exclusively through this
 * interface. State is pushed in from useDeviceComposer (which owns the SSE
 * stream projection); actions call back out to the device relay mutations.
 *
 * Type-ahead queue lives here (purely local, like the desktop store): messages
 * enqueued mid-turn drain one-by-one when isRunning flips back to false.
 */
import type {
  ChatRuntime,
  RuntimeActions,
  RuntimeState,
} from 'jcode-ui-core/runtime';
import { normalizeState } from 'jcode-ui-core/runtime';
import type { ChatImage, QueuedMessage } from 'jcode-ui-core';

export interface DeviceRuntimeCallbacks {
  /** Send a user message through the relay (compose extras applied by the caller). */
  send: (text: string, images?: ChatImage[]) => void;
  stop: () => void;
  /** decision: 'approve' | 'approve_all' | 'deny' (jcode approval vocabulary). */
  respondApproval: (approvalId: string, decision: string) => void;
}

let queueCounter = 0;

export class DeviceChatRuntime implements ChatRuntime {
  private state: RuntimeState = normalizeState({});
  private listeners = new Set<() => void>();

  readonly actions: RuntimeActions;

  constructor(cb: DeviceRuntimeCallbacks) {
    this.actions = {
      sendMessage: (text, images) => cb.send(text, images as ChatImage[] | undefined),
      enqueueMessage: (text, images) => {
        const message: QueuedMessage = {
          id: `q_${Date.now()}_${queueCounter++}`,
          text,
          images: images as ChatImage[] | undefined,
        };
        this.setState({ queued: [...this.state.queued, message] });
      },
      removeQueuedMessage: (id) =>
        this.setState({ queued: this.state.queued.filter((q) => q.id !== id) }),
      stop: () => cb.stop(),
      resolveApproval: (id, approved, approveAll) =>
        cb.respondApproval(id, approved ? (approveAll ? 'approve_all' : 'approve') : 'deny'),
      // The relay exposes no ask_user answer API; ask_user rows render as
      // read-only system notes (threadItems.ts), so this is never invoked.
      submitAskUser: () => {},
      // No edit/branch API on the relay: "edit" resends the text as a new
      // message (documented in the M14 report).
      editMessage: (_id, newText) => cb.send(newText),
    };
  }

  getState = (): RuntimeState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** Push a new state slice; listeners fire synchronously (stable reference). */
  setState(partial: Partial<RuntimeState>): void {
    this.state = { ...this.state, ...partial };
    for (const l of this.listeners) l();
  }

  /** Pop the oldest queued message (queue drain on turn end). */
  shiftQueue(): QueuedMessage | undefined {
    const [head, ...rest] = this.state.queued;
    if (head) this.setState({ queued: rest });
    return head;
  }
}
