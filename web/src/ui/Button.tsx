import type { ButtonHTMLAttributes, ReactNode } from "react";

export type ButtonVariant = "default" | "primary" | "danger" | "ghost" | "icon";
export type ButtonSize = "md" | "sm" | "touch";

export type ButtonProps = {
  variant?: ButtonVariant;
  size?: ButtonSize;
  children?: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>;

export function Button({
  variant = "default",
  size = "md",
  className = "",
  type = "button",
  children,
  ...rest
}: ButtonProps) {
  const classes = [
    "ui-btn",
    `ui-btn-${variant}`,
    size !== "md" ? `ui-btn-${size}` : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button type={type} className={classes} {...rest}>
      {children}
    </button>
  );
}

export function IconButton({
  label,
  className = "",
  children,
  ...rest
}: {
  label: string;
  children: ReactNode;
} & Omit<ButtonProps, "variant" | "children" | "aria-label">) {
  return (
    <Button
      variant="icon"
      aria-label={label}
      title={rest.title ?? label}
      className={`icon-button ${className}`.trim()}
      {...rest}
    >
      {children}
    </Button>
  );
}
