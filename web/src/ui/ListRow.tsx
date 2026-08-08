import type { ButtonHTMLAttributes, ReactNode } from "react";

export function ListRow({
  active,
  leading,
  title,
  meta,
  trailing,
  className = "",
  ...rest
}: {
  active?: boolean;
  leading?: ReactNode;
  title: ReactNode;
  meta?: ReactNode;
  trailing?: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type="button"
      className={`ui-list-row ${active ? "active" : ""} ${className}`.trim()}
      aria-pressed={active}
      {...rest}
    >
      {leading}
      <span className="ui-list-row-main">
        <span className="ui-list-row-title">{title}</span>
        {meta ? <span className="ui-list-row-meta">{meta}</span> : null}
      </span>
      {trailing ? <span className="ui-list-row-trailing">{trailing}</span> : null}
    </button>
  );
}
