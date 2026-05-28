import { useEffect, useRef } from "preact/hooks";

export interface ContextMenuItem {
  label: string;
  onClick: () => void;
  disabled?: boolean;
}

interface Props {
  x: number;
  y: number;
  items: ContextMenuItem[];
  onClose: () => void;
}

// ContextMenu renders a floating, dismissable menu at (x, y). The first
// outside click or Escape dismisses it.
export function ContextMenu({ x, y, items, onClose }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const off = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("mousedown", off);
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("mousedown", off);
      document.removeEventListener("keydown", key);
    };
  }, [onClose]);

  return (
    <div ref={ref} class="ctx-menu" role="menu" style={{ left: `${x}px`, top: `${y}px` }}>
      {items.map((it) => (
        <button
          key={it.label}
          type="button"
          role="menuitem"
          disabled={it.disabled}
          onClick={() => {
            if (!it.disabled) {
              it.onClick();
              onClose();
            }
          }}
        >
          {it.label}
        </button>
      ))}
    </div>
  );
}
