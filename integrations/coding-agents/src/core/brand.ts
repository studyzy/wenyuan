/**
 * "WenYuan" in a brand gradient (#0052D9 -> #00A29A), anchored on the console's primary color so
 * the wordmark matches the product it names. ANSI foreground-only, so it renders anywhere a
 * session banner does.
 */
export function brandWord(): string {
  const start = [0, 82, 217] as const;
  const end = [0, 162, 154] as const;
  const word = "WenYuan";
  let out = "";
  for (let i = 0; i < word.length; i++) {
    const t = i / (word.length - 1);
    const mix = (a: number, b: number) => Math.round(a + (b - a) * t);
    out += `\u001b[38;2;${mix(start[0], end[0])};${mix(start[1], end[1])};${mix(start[2], end[2])}m${word.charAt(i)}`;
  }
  return `${out}\u001b[0m`;
}
