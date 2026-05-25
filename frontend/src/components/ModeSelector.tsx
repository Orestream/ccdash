// ModeSelector — segmented control for a session's answering (permission) mode.
// Changing the mode PATCHes /api/sessions/{id}/mode and reflects the returned session.

import type { PermissionMode } from '../types';

export interface ModeSelectorProps {
  mode: PermissionMode;
  onChange: (mode: PermissionMode) => void;
  disabled?: boolean;
}

const MODES: Array<{ value: PermissionMode; label: string }> = [
  { value: 'default', label: 'Default (ask)' },
  { value: 'acceptEdits', label: 'Edit mode' },
  { value: 'plan', label: 'Plan mode' },
  { value: 'auto', label: 'Auto mode' },
];

export function ModeSelector({ mode, onChange, disabled }: ModeSelectorProps) {
  return (
    <div className="mode-selector" role="radiogroup" aria-label="Answering mode">
      {MODES.map((m) => {
        const active = m.value === mode;
        return (
          <button
            key={m.value}
            type="button"
            role="radio"
            aria-checked={active}
            disabled={disabled}
            className={`mode-option${active ? ' active' : ''}`}
            data-mode={m.value}
            onClick={() => {
              if (!active) onChange(m.value);
            }}
          >
            {m.label}
          </button>
        );
      })}
    </div>
  );
}

export default ModeSelector;
