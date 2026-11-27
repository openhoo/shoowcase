import type { ComponentProps } from "react";
import type { LucideIcon } from "lucide-react";

import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export { ActionTooltipProvider } from "./action-tooltip";

type ActionProps = {
  label: string;
  icon: LucideIcon;
  variant?: ComponentProps<typeof Button>["variant"];
  className?: string;
};

const actionClass = "size-11 min-h-11 min-w-11 justify-self-start p-0";
const glyphClass = "size-5";

export function ActionButton({
  label,
  icon: Icon,
  variant,
  className,
  disabled,
  disabledHint,
  ...props
}: Omit<ComponentProps<"button">, "children" | "aria-label"> &
  ActionProps & { disabledHint?: string }) {
  const control = (
    <Button
      {...props}
      variant={variant}
      disabled={disabled}
      size="icon"
      data-icon-action
      data-action-hint={disabled ? undefined : label}
      aria-label={label}
      className={cn(actionClass, className)}
    >
      <Icon aria-hidden="true" className={glyphClass} />
    </Button>
  );
  // A native disabled button cannot receive keyboard focus or pointer events.
  // Its wrapper provides the explanation without becoming a second action.
  if (disabled) {
    const reason = disabledHint ?? "Select an eligible entry first.";
    return (
      <span
        data-action-hint={label + " · " + reason}
        tabIndex={0}
        role="group"
        aria-label={label + " · " + reason}
        className="inline-flex w-fit justify-self-start rounded-md"
      >
        {control}
      </span>
    );
  }
  return control;
}

export function ActionLink({
  label,
  icon: Icon,
  variant = "outline",
  className,
  ...props
}: Omit<ComponentProps<"a">, "children" | "aria-label"> & ActionProps) {
  return (
    <Button
      asChild
      size="icon"
      variant={variant}
      className={cn(actionClass, className)}
    >
      <a
        {...props}
        data-icon-action
        data-action-hint={label}
        aria-label={label}
      >
        <Icon aria-hidden="true" className={glyphClass} />
      </a>
    </Button>
  );
}

export function ActionSummary({ label, icon: Icon, className }: ActionProps) {
  return (
    <summary
      data-action-hint={label}
      data-icon-action
      aria-label={label}
      className={cn(
        buttonVariants({ variant: "outline", size: "icon" }),
        actionClass,
        "list-none [&::-webkit-details-marker]:hidden",
        className,
      )}
    >
      <Icon aria-hidden="true" className={glyphClass} />
    </summary>
  );
}
