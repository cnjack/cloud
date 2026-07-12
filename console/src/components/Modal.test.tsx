import { useState } from 'react';
import { createPortal } from 'react-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Modal } from './Modal';
import { Select } from './Select';

function ModalHarness({ size = 'default' }: { size?: 'default' | 'wide' }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Launch dialog
      </button>
      <Modal open={open} onClose={() => setOpen(false)} title="Test dialog" size={size}>
        <input aria-label="Dialog input" />
        <button type="button">Last dialog action</button>
      </Modal>
    </>
  );
}

function SelectModalHarness() {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState('one');
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Launch selector dialog
      </button>
      <Modal open={open} onClose={() => setOpen(false)} title="Selector dialog" size="wide">
        <Select
          aria-label="Board"
          value={value}
          onChange={setValue}
          data-testid="dialog-board-select"
          options={Array.from({ length: 24 }, (_, index) => ({
            value: `board-${index + 1}`,
            label: `Board ${index + 1}`,
          }))}
        />
      </Modal>
    </>
  );
}

function ForeignMenuModalHarness() {
  const [open, setOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [portalRoot, setPortalRoot] = useState<HTMLElement | null>(null);
  const openMenu = () => {
    let root = document.getElementById('headlessui-portal-root');
    if (!root) {
      root = document.createElement('div');
      root.id = 'headlessui-portal-root';
      document.body.appendChild(root);
    }
    setPortalRoot(root);
    setMenuOpen(true);
  };
  return (
    <>
      <button type="button" data-foreign-launcher onClick={() => setOpen(true)}>
        Launch board dialog
      </button>
      <Modal open={open} onClose={() => setOpen(false)} title="Board dialog" size="wide">
        <button type="button" onClick={openMenu}>
          Open board menu
        </button>
        {menuOpen && portalRoot && createPortal(
          <div
            role="menu"
            tabIndex={0}
            onKeyDown={(event) => {
              if (event.key !== 'Tab') return;
              // Mirrors Headless UI's tab behavior: consume the event, then
              // use a document-wide focus lookup that can choose an outsider.
              event.preventDefault();
              event.stopPropagation();
              document.querySelector<HTMLButtonElement>('[data-foreign-launcher]')?.focus();
            }}
          >
            <button type="button" role="menuitem">
              Board menu action
            </button>
          </div>,
          portalRoot,
        )}
      </Modal>
    </>
  );
}

describe('Modal', () => {
  let appRoot: HTMLDivElement;
  let rectSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    appRoot = document.createElement('div');
    appRoot.id = 'root';
    document.body.appendChild(appRoot);
    // jsdom reports every element as layout-less. The production filter is
    // correct for hidden modal controls, so give visible test controls a rect.
    rectSpy = vi
      .spyOn(HTMLElement.prototype, 'getClientRects')
      .mockReturnValue([{ width: 1, height: 1 }] as unknown as DOMRectList);
  });

  afterEach(() => {
    rectSpy.mockRestore();
    document.getElementById('headlessui-portal-root')?.remove();
    appRoot.remove();
  });

  it('marks a board-sized dialog as wide', () => {
    render(<ModalHarness size="wide" />, { container: appRoot });
    fireEvent.click(screen.getByRole('button', { name: 'Launch dialog' }));

    expect(screen.getByRole('dialog', { name: 'Test dialog' }).getAttribute('data-size')).toBe('wide');
  });

  it('isolates the app and cycles Tab focus inside the dialog before restoring the launcher', () => {
    render(<ModalHarness />, { container: appRoot });
    const launch = screen.getByRole('button', { name: 'Launch dialog' });
    launch.focus();
    fireEvent.click(launch);

    const dialog = screen.getByRole('dialog', { name: 'Test dialog' });
    const close = screen.getByRole('button', { name: 'Close dialog' });
    const last = screen.getByRole('button', { name: 'Last dialog action' });
    expect(appRoot.hasAttribute('inert')).toBe(true);
    expect(appRoot.getAttribute('aria-hidden')).toBe('true');

    last.focus();
    fireEvent.keyDown(document, { key: 'Tab' });
    expect(document.activeElement).toBe(close);
    expect(dialog.contains(document.activeElement)).toBe(true);

    close.focus();
    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(last);

    fireEvent.click(close);
    expect(appRoot.hasAttribute('inert')).toBe(false);
    expect(appRoot.hasAttribute('aria-hidden')).toBe(false);
    expect(document.activeElement).toBe(launch);
  });

  it('keeps a long selector menu inside a wide dialog boundary on a short screen', async () => {
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 500 });
    render(<SelectModalHarness />, { container: appRoot });
    fireEvent.click(screen.getByRole('button', { name: 'Launch selector dialog' }));
    fireEvent.click(screen.getByTestId('dialog-board-select'));

    const dialog = screen.getByRole('dialog', { name: 'Selector dialog' });
    const lastOption = await screen.findByRole('option', { name: 'Board 24' });
    // Headless UI's anchored menu is portaled to this dialog (not document.body),
    // so it stays within aria-modal/focus-trap ownership even on short windows.
    expect(dialog.contains(lastOption)).toBe(true);
  });

  it('keeps Tab and Shift+Tab from an open selector inside the dialog', async () => {
    render(<SelectModalHarness />, { container: appRoot });
    fireEvent.click(screen.getByRole('button', { name: 'Launch selector dialog' }));
    const trigger = screen.getByTestId('dialog-board-select');
    const close = screen.getByRole('button', { name: 'Close dialog' });

    fireEvent.click(trigger);
    const listbox = await screen.findByRole('listbox');
    listbox.focus();
    fireEvent.keyDown(listbox, { key: 'Tab' });
    await waitFor(() => expect(document.activeElement).toBe(close));

    fireEvent.click(trigger);
    const reopenedListbox = await screen.findByRole('listbox');
    reopenedListbox.focus();
    fireEvent.keyDown(reopenedListbox, { key: 'Tab', shiftKey: true });
    await waitFor(() => {
      const dialog = screen.getByRole('dialog', { name: 'Selector dialog' });
      expect(dialog.contains(document.activeElement)).toBe(true);
    });
  });

  it('adopts an embedded board menu portal and recovers its stopped Tab event', async () => {
    render(<ForeignMenuModalHarness />, { container: appRoot });
    fireEvent.click(screen.getByRole('button', { name: 'Launch board dialog' }));
    fireEvent.click(screen.getByRole('button', { name: 'Open board menu' }));

    const dialog = screen.getByRole('dialog', { name: 'Board dialog' });
    const menu = await screen.findByRole('menu');
    await waitFor(() => expect(dialog.contains(menu)).toBe(true));
    menu.focus();
    fireEvent.keyDown(menu, { key: 'Tab' });

    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    const firstPortalRoot = document.getElementById('headlessui-portal-root');
    // Headless UI removes its empty global root after a popup closes. A later
    // board menu then creates a new root; it must be adopted too, not left at
    // document.body because the first root was already handled.
    firstPortalRoot?.remove();
    fireEvent.click(screen.getByRole('button', { name: 'Open board menu' }));
    let replacementPortalRoot: HTMLElement | null = null;
    await waitFor(() => {
      replacementPortalRoot = document.getElementById('headlessui-portal-root');
      expect(replacementPortalRoot).not.toBe(firstPortalRoot);
      expect(replacementPortalRoot).not.toBeNull();
      expect(dialog.contains(replacementPortalRoot)).toBe(true);
    });
    const replacementMenu = await screen.findByRole('menu');
    replacementMenu.focus();
    fireEvent.keyDown(replacementMenu, { key: 'Tab' });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    fireEvent.click(screen.getByRole('button', { name: 'Close dialog' }));
    await waitFor(() => expect(replacementPortalRoot?.parentElement).toBe(document.body));
  });
});
