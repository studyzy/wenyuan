/**
 * Streaming JSONL reading for transcript readers.
 *
 * Reading a whole transcript with readFileSync and splitting on newlines dies past V8's maximum
 * string length (~537M chars) before a single record is parsed, and a reader that treats that
 * throw as "unreadable file" reports no turns — the session ends up retained as nothing. Streaming
 * a chunk at a time bounds memory by the largest record plus whatever the reader keeps.
 *
 * The file is always read WHOLE. A sliding tail window would shift every turn's index on each
 * read, breaking the absolute turn count and prefix fingerprint an append cursor relies on — any
 * session over the cap would re-send its entire transcript every turn, forever.
 */
import { closeSync, openSync, readSync } from "node:fs";
import { StringDecoder } from "node:string_decoder";

/** Read granularity: large enough that a multi-MB transcript is a few hundred syscalls. */
const CHUNK_BYTES = 64 * 1024;

/**
 * Every record of a JSONL file, oldest first, one complete line at a time.
 *
 * Fail-open like the readers it serves: a missing or unopenable file yields no records rather
 * than throwing. The fd is opened lazily (on first iteration) and closed even if the consumer
 * abandons the generator early — for..of calls return() for us.
 */
export function* readJsonl(path: string): Generator<string> {
  let fd: number;
  try {
    fd = openSync(path, "r");
  } catch {
    return;
  }
  try {
    const buffer = Buffer.allocUnsafe(CHUNK_BYTES);
    // StringDecoder holds back a trailing partial UTF-8 sequence, so a multi-byte character split
    // across two chunks is reassembled instead of becoming two replacement chars.
    const decoder = new StringDecoder("utf8");
    let pending = "";
    let position = 0;

    for (;;) {
      const bytesRead = readSync(fd, buffer, 0, buffer.length, position);
      if (bytesRead <= 0) break;
      position += bytesRead;
      pending += decoder.write(buffer.subarray(0, bytesRead));

      let newline: number;
      while ((newline = pending.indexOf("\n")) !== -1) {
        yield pending.slice(0, newline);
        pending = pending.slice(newline + 1);
      }
    }

    pending += decoder.end();
    // A final record with no trailing newline still counts.
    if (pending) yield pending;
  } finally {
    closeSync(fd);
  }
}
