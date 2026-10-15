"use client";

/**
 * Render retained text that may carry inline-attachment placeholders.
 *
 * Text retained with inline attachments stores an atomic placeholder —
 * a bracketed token carrying the attachment's short id — at each position an
 * attachment occupied. That token is the real stored content rather than a
 * rendering artefact, but showing it raw would mean nothing to a reader: the
 * point of retaining attachments inline is that they belong in position, beside
 * the prose that refers to them.
 *
 * So the text is split on those placeholders and each one becomes what it
 * stands for: a picture for an image, a file card for anything else.
 *
 * The API returns the metadata alongside the text, so pass it in. It is what
 * says whether an id is an image to draw or a file to link; without it every
 * attachment has to be guessed at as an image.
 */

import { useState } from "react";
import { FileText, ImageOff } from "lucide-react";

/** Matches an attachment placeholder and captures its short id. */
const PLACEHOLDER_RE = /⟦hs-att:([0-9a-f]{12})⟧/g;

export interface RetainedAttachment {
  id: string;
  hash?: string;
  kind?: string;
  media_type?: string;
  byte_size?: number;
  filename?: string;
  url?: string;
}

/** True when the text contains at least one attachment placeholder. */
export function hasInlineAttachment(text: string): boolean {
  // A /g regex carries lastIndex between calls, so build a fresh matcher.
  return new RegExp(PLACEHOLDER_RE.source).test(text);
}

function formatBytes(bytes?: number): string {
  if (!bytes && bytes !== 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/**
 * Where the browser fetches an attachment.
 *
 * Always through the frontend proxy, never the `url` the API returned: that one
 * points at the upstream service, which a browser cannot authenticate against.
 * The proxy attaches the credentials server-side.
 */
function attachmentUrl(bankId: string, attachment: RetainedAttachment): string {
  return `/api/banks/${encodeURIComponent(bankId)}/attachments/${attachment.id}`;
}

function Unavailable({ label }: { label: string }) {
  // The bytes can legitimately be gone, reclaimed or stranded by a storage
  // change under an old document. Say so where it belonged rather than leaving
  // a silent gap the reader cannot interpret.
  return (
    <span className="inline-flex items-center gap-1.5 my-1 px-2 py-1 rounded border border-dashed border-border text-[10px] text-muted-foreground align-middle">
      <ImageOff className="h-3 w-3" />
      {label}
    </span>
  );
}

function FileCard({ bankId, attachment }: { bankId: string; attachment: RetainedAttachment }) {
  const name = attachment.filename || attachment.media_type || "attachment";
  const detail = [
    attachment.filename ? attachment.media_type : null,
    formatBytes(attachment.byte_size),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <a
      href={attachmentUrl(bankId, attachment)}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1.5 my-1.5 px-2 py-1 rounded border border-border bg-muted/40 hover:bg-muted transition-colors no-underline max-w-[280px] align-middle"
    >
      <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        <span className="block text-[11px] font-medium text-foreground truncate">{name}</span>
        {detail && (
          <span className="block text-[10px] text-muted-foreground truncate">{detail}</span>
        )}
      </span>
    </a>
  );
}

function InlineImage({ bankId, attachment }: { bankId: string; attachment: RetainedAttachment }) {
  const [failed, setFailed] = useState(false);
  if (failed) return <Unavailable label={`image unavailable (${attachment.id})`} />;
  const href = attachmentUrl(bankId, attachment);
  return (
    // Wrapped in a link so the preview doubles as the way to open the full
    // image: it is scaled down to sit in a document body, and a reader who
    // wants to inspect it has to be able to click through.
    <a href={href} target="_blank" rel="noreferrer" className="inline-block">
      {/* A plain img rather than the framework image component: the bytes are
          proxied at request time behind server-side auth, so there is no stable
          URL an optimiser could resolve. */}
      <img
        src={href}
        alt={attachment.filename || "Retained inline attachment"}
        title={attachment.filename || attachment.media_type}
        onError={() => setFailed(true)}
        // Small on purpose: these sit inside a document body or beside a fact,
        // where a full-width screenshot would push the text they belong to off
        // screen. Clicking opens the original at full size.
        className="block my-1.5 max-h-40 max-w-[280px] rounded border border-border object-contain cursor-zoom-in hover:border-foreground/30 transition-colors"
      />
    </a>
  );
}

function InlineAttachment({
  bankId,
  attachment,
}: {
  bankId: string;
  attachment: RetainedAttachment;
}) {
  // Only treat it as an image when there is no metadata at all, which is the
  // older shape; an image that fails degrades to the same unavailable note.
  const isImage = attachment.kind
    ? attachment.kind === "image"
    : (attachment.media_type ?? "image/").startsWith("image/");
  return isImage ? (
    <InlineImage bankId={bankId} attachment={attachment} />
  ) : (
    <FileCard bankId={bankId} attachment={attachment} />
  );
}

export function InlineAttachmentText({
  text,
  bankId,
  attachments,
  className,
}: {
  text: string;
  bankId: string;
  /** Metadata for the ids in `text`, as returned on `attachments[]`. */
  attachments?: RetainedAttachment[];
  className?: string;
}) {
  const byId = new Map((attachments ?? []).map((a) => [a.id, a]));
  const nodes: React.ReactNode[] = [];
  const matcher = new RegExp(PLACEHOLDER_RE.source, "g");
  let cursor = 0;
  let match: RegExpExecArray | null;

  while ((match = matcher.exec(text)) !== null) {
    if (match.index > cursor) nodes.push(text.slice(cursor, match.index));
    const id = match[1];
    nodes.push(
      <InlineAttachment
        key={`${id}-${match.index}`}
        bankId={bankId}
        attachment={byId.get(id) ?? { id }}
      />
    );
    cursor = match.index + match[0].length;
  }
  nodes.push(text.slice(cursor));

  return <div className={className}>{nodes}</div>;
}

/**
 * A standalone strip of attachments, for places with no placeholders to expand.
 *
 * A memory's text deliberately carries no placeholder, describing the
 * attachment in prose instead, so its attachments cannot be rendered in
 * position. They are shown as a strip beside the fact instead, so the reader
 * still sees what the model had in view when it produced it.
 */
export function AttachmentStrip({
  bankId,
  attachments,
  className,
}: {
  bankId: string;
  attachments?: RetainedAttachment[];
  className?: string;
}) {
  if (!attachments?.length) return null;
  return (
    <div className={className}>
      <div className="flex flex-wrap gap-2 items-start">
        {attachments.map((attachment) => (
          <InlineAttachment key={attachment.id} bankId={bankId} attachment={attachment} />
        ))}
      </div>
    </div>
  );
}
