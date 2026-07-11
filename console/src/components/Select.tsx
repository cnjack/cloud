/*
 * Select — the console's <select> replacement, built on Headless UI's Listbox
 * so the options popup renders with theme tokens (a native dropdown ignores
 * CSS and looks foreign on both dark and light). Every select in the console
 * goes through this component; SelectField in Field.tsx adds the label / hint /
 * error chrome around it.
 *
 * The options panel is anchored (portaled) so it escapes overflow clipping in
 * modals and scroll containers. `className` styles the trigger button — page
 * modules use it the same way they styled the old native <select>; any other
 * button prop (id, style, title, aria-*, data-testid) passes through to the
 * trigger.
 */
import {
  Listbox,
  ListboxButton,
  ListboxOption,
  ListboxOptions,
} from '@headlessui/react';
import type { ComponentPropsWithoutRef } from 'react';
import styles from './Select.module.css';

export interface SelectOption {
  value: string;
  label: string;
}

export interface SelectProps
  extends Omit<
    ComponentPropsWithoutRef<'button'>,
    'value' | 'onChange' | 'children'
  > {
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  /** Trigger text when `value` matches no option (e.g. nothing picked yet). */
  placeholder?: string;
  'data-testid'?: string;
}

export function Select({
  value,
  onChange,
  options,
  placeholder,
  disabled,
  className,
  ...rest
}: SelectProps) {
  const current = options.find((o) => o.value === value);
  return (
    <Listbox
      value={value}
      // A native select only fires change on an actual change; the Listbox
      // fires on every pick. Swallow same-value re-picks so mutation-wired
      // handlers (role change, default model) don't issue redundant writes.
      onChange={(next: string) => {
        if (next !== value) onChange(next);
      }}
      disabled={disabled}
    >
      <ListboxButton
        className={[styles.trigger, className].filter(Boolean).join(' ')}
        {...rest}
      >
        <span className={current ? styles.value : styles.placeholder}>
          {current ? current.label : (placeholder ?? '\u00A0')}
        </span>
      </ListboxButton>
      <ListboxOptions
        modal={false}
        anchor={{ to: 'bottom start', gap: 4 }}
        className={styles.options}
      >
        {options.map((o) => (
          <ListboxOption key={o.value} value={o.value} className={styles.option}>
            {o.label}
          </ListboxOption>
        ))}
      </ListboxOptions>
    </Listbox>
  );
}
