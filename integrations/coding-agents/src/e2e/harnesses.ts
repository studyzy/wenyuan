/**
 * Docker E2E setups for every host CLI this package integrates with.
 *
 * Each entry is small and declarative: where the host keeps that CLI's SUBSCRIPTION credentials,
 * where the CLI expects them inside the container, how to wire this package into it, and how to
 * drive it non-interactively. Everything else — the seeded bank, the git fixture, the assertions —
 * is shared in ./harness. A host case is one entry here plus its own Dockerfile under e2e/.
 *
 * The roster lands with the host wave: each new host appends one setup object to the list below,
 * and the shared suite in tests/e2e picks it up unchanged.
 */
import type { HarnessDockerSetup } from "./harness";

/** Every harness the unified Docker E2E can drive, in a stable order. */
export const ALL_HARNESS_SETUPS: HarnessDockerSetup[] = [];
