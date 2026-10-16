import * as React from "react";

import { cn } from "../../lib/utils";

const Textarea = React.forwardRef<HTMLTextAreaElement, React.ComponentProps<"textarea">>(
  ({ className, ...props }, ref) => (
    <textarea
      className={cn(
        "flex min-h-[80px] w-full rounded-[9px] border border-input bg-card px-3 py-2 text-[13px] leading-[19px] shadow-[inset_0_1px_2px_0_rgba(9,20,44,0.04)] placeholder:text-muted-foreground transition-[border-color,box-shadow] duration-200 ease-[var(--ease-out-quint)] hover:border-primary/30 focus-visible:outline-none focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/25 disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      ref={ref}
      {...props}
    />
  ),
);
Textarea.displayName = "Textarea";

export { Textarea };
