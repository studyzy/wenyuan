/**
 * Clipboard access for the console.
 *
 * `navigator.clipboard` only exists in a secure context (HTTPS or localhost),
 * so a console served over plain HTTP on a LAN address has no Clipboard API at
 * all. Optional-chaining onto it therefore makes a copy button silently do
 * nothing, which is what happened to the one-time key display. The deprecated
 * selection-based path keeps the button working on that deployment shape.
 */
export async function copyText(text: string): Promise<boolean> {
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  if (clipboard && typeof clipboard.writeText === "function") {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // Permission denied or a document that is not focused: fall through to
      // the selection path rather than reporting a copy that did not happen.
    }
  }
  if (typeof document === "undefined") return false;
  return copyViaSelection(text);
}

function copyViaSelection(text: string): boolean {
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.width = "1px";
  textarea.style.height = "1px";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  const focused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  document.body.appendChild(textarea);
  let copied = false;
  try {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    copied = typeof document.execCommand === "function" && document.execCommand("copy");
  } catch {
    copied = false;
  } finally {
    document.body.removeChild(textarea);
    focused?.focus();
  }
  return copied;
}