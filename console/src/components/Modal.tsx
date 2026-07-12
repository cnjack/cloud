import { useEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { Portal } from '@headlessui/react';
import styles from './Modal.module.css';

interface ModalProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** Default is form-sized; wide is a bounded workspace surface (Kanban). */
  size?: 'default' | 'wide';
  'data-testid'?: string;
}

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  size = 'default',
  'data-testid': testId,
}: ModalProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const foreignPortalHostRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  // Keep the latest onClose without re-running the open effect: callers often
  // pass an inline handler whose identity changes every render (e.g. on each
  // keystroke), and we must not re-focus/re-bind on those re-renders.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    restoreFocusRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;

    // A portal sits beside #root. Marking the app shell inert prevents both
    // mouse and keyboard interaction from leaking beneath aria-modal dialogs.
    const appRoot = document.getElementById('root') as (HTMLElement & { inert?: boolean }) | null;
    const previousInert = appRoot?.inert ?? false;
    const previousInertAttribute = appRoot?.hasAttribute('inert') ?? false;
    const previousAriaHidden = appRoot?.getAttribute('aria-hidden') ?? null;
    if (appRoot) {
      // Set the attribute as well as the property: the attribute is the
      // standards-level inert signal and leaves a reliable fallback footprint
      // in older embedded browser runtimes. The focus trap below is the final
      // keyboard backstop where `inert` itself is unavailable.
      appRoot.inert = true;
      appRoot.setAttribute('inert', '');
      appRoot.setAttribute('aria-hidden', 'true');
    }

    // jtype-board-react bundles its own Headless UI runtime. Its anchored
    // menus therefore cannot see this console's Portal.Group and use the
    // shared body-level `#headlessui-portal-root` instead. While a modal is
    // open, adopt that one root into this dialog so the embedded board's menus
    // stay inside the aria-modal/focus boundary too. The observer also catches
    // a root that Headless UI creates lazily on the first board-menu open.
    const portalRootId = 'headlessui-portal-root';
    const foreignPortalHost = foreignPortalHostRef.current;
    const adoptedPortalRoots: Array<{
      element: HTMLElement;
      parent: Node | null;
      nextSibling: ChildNode | null;
    }> = [];
    const adoptForeignPortalRoot = () => {
      const root = document.getElementById(portalRootId);
      if (!foreignPortalHost || !root || root.parentElement === foreignPortalHost) return;
      if (!adoptedPortalRoots.some((entry) => entry.element === root)) {
        adoptedPortalRoots.push({
          element: root,
          parent: root.parentNode,
          nextSibling: root.nextSibling,
        });
      }
      foreignPortalHost.append(root);
    };
    adoptForeignPortalRoot();
    const portalObserver = new MutationObserver(adoptForeignPortalRoot);
    // Headless UI creates the shared root as a direct child of body, so avoid
    // observing every board/card mutation while the workspace is open.
    portalObserver.observe(document.body, { childList: true });

    const getFocusable = () => {
      const panel = panelRef.current;
      if (!panel) return [] as HTMLElement[];
      return Array.from(
        panel.querySelectorAll<HTMLElement>(
          'a[href], area[href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((element) => !element.hidden && element.getClientRects().length > 0);
    };

    const isHeadlessPopupKeyEvent = (e: KeyboardEvent) => {
      const target = e.target instanceof Element ? e.target : null;
      const active = document.activeElement instanceof Element ? document.activeElement : null;
      return !!(
        target?.closest('[role="listbox"], [role="menu"]') ||
        active?.closest('[role="listbox"], [role="menu"]')
      );
    };

    const onKey = (e: KeyboardEvent) => {
      // Headless UI's anchored Listbox/Menu owns Escape/Tab and deliberately
      // stops propagation. Listen in capture so we can leave Escape to the
      // popup (close it, not the dialog) and, after its Tab handling runs,
      // recover focus if its document-wide next/previous lookup escaped here.
      if (isHeadlessPopupKeyEvent(e)) {
        if (e.key === 'Tab') {
          const backwards = e.shiftKey;
          queueMicrotask(() => {
            const panel = panelRef.current;
            const active = document.activeElement;
            if (!panel || (active instanceof Node && panel.contains(active))) return;
            const focusable = getFocusable();
            (backwards ? focusable.at(-1) : focusable[0])?.focus();
          });
        }
        return;
      }

      if (e.key === 'Escape') onCloseRef.current();
      if (e.key !== 'Tab') return;
      const focusable = getFocusable();
      if (!focusable.length) {
        e.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!first || !last) return;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey, true);
    // Focus a form field first so keyboard users land in the body; fall back to
    // any focusable control. Querying plain document order would grab the
    // header's close button, which precedes the body in the DOM. Selects render
    // as Headless UI listbox trigger buttons, so match those alongside inputs.
    const panel = panelRef.current;
    const target =
      panel?.querySelector<HTMLElement>(
        'input:not([disabled]), textarea:not([disabled]), select:not([disabled]), button[aria-haspopup="listbox"]:not([disabled])',
      ) ?? panel?.querySelector<HTMLElement>('button, [tabindex]');
    target?.focus();
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey, true);
      portalObserver.disconnect();
      for (const { element, parent, nextSibling } of adoptedPortalRoots) {
        // Headless UI removes an empty root when its last popup closes. Do not
        // resurrect that stale node, but restore a root still hosted here even
        // when React has just detached the whole modal subtree.
        if (!element.isConnected && element.parentElement !== foreignPortalHost) continue;
        if (parent?.isConnected) {
          parent.insertBefore(element, nextSibling?.parentNode === parent ? nextSibling : null);
        } else {
          document.body.append(element);
        }
      }
      document.body.style.overflow = prevOverflow;
      if (appRoot) {
        appRoot.inert = previousInert;
        if (previousInertAttribute) appRoot.setAttribute('inert', '');
        else appRoot.removeAttribute('inert');
        if (previousAriaHidden === null) appRoot.removeAttribute('aria-hidden');
        else appRoot.setAttribute('aria-hidden', previousAriaHidden);
      }
      if (restoreFocusRef.current?.isConnected) {
        restoreFocusRef.current.focus({ preventScroll: true });
      }
      restoreFocusRef.current = null;
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <div
      className={styles.overlay}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className={styles.panel}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        ref={panelRef}
        data-size={size}
        data-testid={testId}
      >
        {/* Anchored Headless UI controls normally portal to document.body. Keep
            their menus inside the dialog so aria-modal, inert and the focus
            trap describe one coherent interaction boundary. */}
        <Portal.Group target={panelRef}>
          <header className={styles.header}>
            <h2 className={styles.title}>{title}</h2>
            <button
              className={styles.close}
              onClick={onClose}
              aria-label="Close dialog"
              type="button"
            >
              ✕
            </button>
          </header>
          <div className={styles.body}>{children}</div>
          {footer && <footer className={styles.footer}>{footer}</footer>}
        </Portal.Group>
        {/* Host for an embedded dependency's separate Headless UI portal root.
            It is intentionally inside the dialog, but visually inert until a
            board menu opens and its root is adopted by the effect above. */}
        <div ref={foreignPortalHostRef} className={styles.foreignPortalHost} />
      </div>
    </div>,
    document.body,
  );
}
