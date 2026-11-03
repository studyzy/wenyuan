/**
 * configureBank — apply the bank manifest (missions, retain strategies, entity labels), then
 * seed knowledge pages when the server supports them. Both halves are idempotent and ADDITIVE:
 * nothing the bank already says is overwritten, bar the extraction mode of the package's own
 * strategies — so the deepen engine can re-run this every pass.
 */

import {
  type BankOverrides,
  codingBankManifest,
  type CustomPagesConfig,
  type PagesConfig,
  type PageTrigger,
  type RetainExtractionMode,
} from "../missions";
import { seedPages } from "./knowledge";
import type { ClientInternals } from "./transport";

export interface ConfigureBankOpts {
  reset?: boolean;
  pageTrigger?: PageTrigger;
  /** Which pages to seed and with what query. */
  pages?: PagesConfig;
  /** Pages of the user's own to seed alongside them. */
  customPages?: CustomPagesConfig;
  /** false skips the config half entirely, for a bank whose owner shapes it themselves. That
   *  bank should then define the strategies this package writes under (git, gitlog, conversation,
   *  document, survey): an unknown strategy name is not an error server-side, it just falls back
   *  to the bank's own config, so the miss is silent. */
  manage?: boolean;
  /** Extraction mode for the package's own strategies. */
  extractionMode?: RetainExtractionMode;
  /** Bank-config fields to add where the bank is silent. */
  defaults?: Record<string, unknown>;
}

/**
 * Configure the bank. Creates it if missing; legacy servers continue with the template-only path.
 */
export async function configureBank(c: ClientInternals, opts: ConfigureBankOpts = {}): Promise<void> {
  if (opts.reset) {
    await c.req("DELETE", c.bankUrl());
    c.log(`[bank] reset ${c.bank}`);
  }
  if (opts.manage === false) {
    c.log(`[bank] manageBankConfig: false — leaving ${c.bank}'s configuration alone`);
  } else {
    // What the bank ALREADY overrides decides what is left to write: the missions are seeded once
    // and then belong to whoever set them, and every other field is added only where the bank is
    // silent — bar the extraction mode of the package's own strategies, which is re-synced to
    // the configured mode. A reset just deleted the bank, so there is nothing to read.
    const manifest = codingBankManifest(
      opts.reset ? undefined : await readBankOverrides(c),
      opts.extractionMode,
      opts.defaults
    );
    if (!manifest) {
      c.log(`[bank] ${c.bank} already carries the coding structure — nothing to apply`);
    } else {
      await c.req("POST", c.bankUrl("/import"), manifest);
      c.log(`[bank] applied to ${c.bank}: ${Object.keys(manifest.bank).sort().join(", ")}`);
    }
  }
  await seedPages(c, opts.pageTrigger, opts.pages, opts.customPages);
}

/**
 * This bank's own config OVERRIDES, or undefined when there are none to read.
 *
 * Deliberately the overrides and not the resolved config: an inherited global default is not
 * something this bank's owner chose, so it must not read as "already set". undefined means the
 * bank does not exist yet, or the deployment has the bank-config API switched off — in neither
 * case can anything have been customised, so the caller seeds the full template.
 */
async function readBankOverrides(c: ClientInternals): Promise<BankOverrides | undefined> {
  try {
    const r = await c.req("GET", c.bankUrl("/config"));
    if (!r.ok) return undefined;
    const j = (await r.json()) as { overrides?: BankOverrides };
    return j.overrides ?? {};
  } catch {
    return undefined;
  }
}
