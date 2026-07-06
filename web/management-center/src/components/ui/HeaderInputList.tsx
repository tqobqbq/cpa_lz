import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import type { HeaderEntry } from '@/utils/headers';

interface HeaderInputListProps {
  entries: HeaderEntry[];
  onChange: (entries: HeaderEntry[]) => void;
  addLabel: string;
  disabled?: boolean;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  removeButtonTitle?: string;
  removeButtonAriaLabel?: string;
}

export function HeaderInputList({
  entries,
  onChange,
  addLabel,
  disabled = false,
  keyPlaceholder = 'X-Custom-Header',
  valuePlaceholder = 'value',
  removeButtonTitle = 'Remove',
  removeButtonAriaLabel = 'Remove',
}: HeaderInputListProps) {
  const updateEntry = (index: number, field: 'key' | 'value', value: string) => {
    const next = entries.map((entry, idx) => (idx === index ? { ...entry, [field]: value } : entry));
    onChange(next);
  };

  const addEntry = () => {
    onChange([...entries, { key: '', value: '' }]);
  };

  const removeEntry = (index: number) => {
    onChange(entries.filter((_, idx) => idx !== index));
  };

  return (
    <div className="header-input-list">
      {entries.map((entry, index) => (
        <Fragment key={index}>
          <div className="header-input-row">
            <input
              className="input"
              placeholder={keyPlaceholder}
              value={entry.key}
              onChange={(e) => updateEntry(index, 'key', e.target.value)}
              disabled={disabled}
            />
            <span className="header-separator">:</span>
            <input
              className="input"
              placeholder={valuePlaceholder}
              value={entry.value}
              onChange={(e) => updateEntry(index, 'value', e.target.value)}
              disabled={disabled}
            />
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeEntry(index)}
              disabled={disabled}
              title={removeButtonTitle}
              aria-label={removeButtonAriaLabel}
            >
              <IconX size={14} />
            </Button>
          </div>
        </Fragment>
      ))}
      <Button variant="secondary" size="sm" onClick={addEntry} disabled={disabled} className="align-start">
        {addLabel}
      </Button>
    </div>
  );
}
