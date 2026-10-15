import { cn } from "../../lib/utils";
import { withBasePath } from "../../lib/base-path";

/**
 * Shared loading spinner: the product mark as a small animated mascot, so every
 * wait state looks the same instead of one icon here and another there.
 *
 * Two motions. "flip" turns in place with a slight squash and only a tiny bob,
 * which keeps it safe inside a button or inline next to text. "jump" uses the
 * full tumble — crouch, hop with a flip, landing squash — for prominent centred
 * loading states that have vertical room. The keyframes live in the stylesheet.
 */
const SIZES = {
  xs: "w-3.5 h-3.5",
  sm: "w-4 h-4",
  md: "w-6 h-6",
  lg: "w-8 h-8",
  xl: "w-14 h-14",
} as const;

export type SpinnerSize = keyof typeof SIZES;

export function Spinner({
  size = "md",
  variant = "flip",
  className,
}: {
  size?: SpinnerSize;
  variant?: "flip" | "jump";
  className?: string;
}) {
  return (
    // A raw img rather than the framework image component: that wrapper adds a
    // positioned element which fights the transform animation, and buys nothing
    // for a tiny static asset.
    <img
      src={withBasePath("/favicon.png")}
      alt=""
      role="status"
      aria-label="Loading"
      className={cn(
        SIZES[size],
        variant === "jump" ? "animate-logo-tumble" : "animate-logo-flip",
        "inline-block select-none object-contain",
        className,
      )}
    />
  );
}
