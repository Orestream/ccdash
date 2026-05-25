import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { ModeSelector } from './ModeSelector';

describe('ModeSelector', () => {
  it('highlights the current mode', () => {
    render(<ModeSelector mode="plan" onChange={() => {}} />);
    const planBtn = screen.getByRole('radio', { name: 'Plan mode' });
    expect(planBtn).toHaveClass('active');
    expect(planBtn).toHaveAttribute('aria-checked', 'true');

    const defaultBtn = screen.getByRole('radio', { name: 'Default (ask)' });
    expect(defaultBtn).not.toHaveClass('active');
    expect(defaultBtn).toHaveAttribute('aria-checked', 'false');
  });

  it('fires onChange with the selected mode', () => {
    const onChange = vi.fn();
    render(<ModeSelector mode="default" onChange={onChange} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Auto mode' }));
    expect(onChange).toHaveBeenCalledWith('auto');
  });

  it('does not fire onChange when clicking the already-active mode', () => {
    const onChange = vi.fn();
    render(<ModeSelector mode="acceptEdits" onChange={onChange} />);
    fireEvent.click(screen.getByRole('radio', { name: 'Edit mode' }));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('disables all options when disabled', () => {
    render(<ModeSelector mode="default" onChange={() => {}} disabled />);
    for (const label of ['Default (ask)', 'Edit mode', 'Plan mode', 'Auto mode']) {
      expect(screen.getByRole('radio', { name: label })).toBeDisabled();
    }
  });
});
