// GitModeSelector — segmented control for a project's git-mode (worktree vs
// default). Visual match for ModeSelector but lives on the project (not the
// session), since this is a per-project setting.

import type { ProjectGitMode } from '../types';

export interface GitModeSelectorProps {
  mode: ProjectGitMode;
  onChange: (mode: ProjectGitMode) => void;
  disabled?: boolean;
  // When false, the per-mode hint line is omitted (callers can use the active
  // hint as a tooltip instead). Defaults to true for the project-page form.
  showHint?: boolean;
}

const MODES: Array<{ value: ProjectGitMode; label: string; hint: string }> = [
  {
    value: 'worktree',
    label: 'Worktree',
    hint: 'Each session runs in its own checkout on a ccdash/* branch. Use this when you want parallel isolated sessions.',
  },
  {
    value: 'default',
    label: 'Direct',
    hint: 'Sessions edit the project directory directly. Simpler, but sessions can collide.',
  },
];

export function GitModeSelector({
  mode,
  onChange,
  disabled,
  showHint = true,
}: GitModeSelectorProps) {
  const activeHint = MODES.find((m) => m.value === mode)?.hint ?? '';
  return (
    <>
      <div
        className="git-mode-selector"
        role="radiogroup"
        aria-label="Git mode"
        title={showHint ? undefined : activeHint}
      >
        {MODES.map((m) => {
          const active = m.value === mode;
          return (
            <button
              key={m.value}
              type="button"
              role="radio"
              aria-checked={active}
              disabled={disabled}
              className={`git-mode-option${active ? ' active' : ''}`}
              data-git-mode={m.value}
              title={m.hint}
              onClick={() => {
                if (!active) onChange(m.value);
              }}
            >
              {m.label}
            </button>
          );
        })}
      </div>
      {showHint && <span className="git-mode-hint">{activeHint}</span>}
    </>
  );
}

export default GitModeSelector;
