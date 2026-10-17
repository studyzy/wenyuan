import { describe, expect, it } from "vitest";
import { copyFile, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME } from "../../src/runtime/layout";
import {
  packageRoot,
  runInstaller,
  withFakeHome,
  type FakeHome,
} from "../helpers/fake-home";

/**
 * Update re-stages the packaged runtime and writes no host config at all.
 *
 * Two package locations are fabricated so the second one can hold a file the
 * first lacked and lack a file the first had: staging replaces the runtime
 * wholesale, so only the newer inventory survives.
 */

const CONFIG = ".claude/settings.json";

/** A stand-in package location: a real built entry point and its own runtime. */
async function fakePackage(files: readonly string[]): Promise<string> {
  const root: string = await mkdtemp(join(tmpdir(), "wenyuan-fake-package-"));
  await mkdir(join(root, "dist", "cli"), { recursive: true });
  await copyFile(
    join(packageRoot(), "dist", "cli", "main.mjs"),
    join(root, "dist", "cli", "main.mjs"),
  );
  for (const file of files) {
    const target: string = join(root, "dist", "runtime", file);
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, `// ${file}\n`);
  }
  // A package location carries the companion skill too, so the stand-in is a
  // faithful one rather than one that only happens to stage.
  await mkdir(join(root, "skill"), { recursive: true });
  await writeFile(join(root, "skill", "SKILL.md"), "# companion skill\n");
  return root;
}

async function withPackages<T>(
  olderFiles: readonly string[],
  newerFiles: readonly string[],
  body: (older: string, newer: string) => Promise<T>,
): Promise<T> {
  const older: string = await fakePackage(olderFiles);
  const newer: string = await fakePackage(newerFiles);
  try {
    return await body(older, newer);
  } finally {
    await rm(older, { recursive: true, force: true });
    await rm(newer, { recursive: true, force: true });
  }
}

describe("updating after an install", () => {
  it("refreshes the runtime and leaves the host config byte-identical", async () => {
    await withPackages(
      ["hooks/session-start.mjs", "older-only.txt"],
      ["hooks/session-start.mjs", "newer-only.txt"],
      async (older: string, newer: string) => {
        await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
          expect((await runInstaller(["install", "claude-code"], { packageRoot: older })).status).toBe(0);
          expect(await home.exists(`${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/older-only.txt`)).toBe(true);
          const before: string = await home.read(CONFIG);

          expect((await runInstaller(["update"], { packageRoot: newer })).status).toBe(0);

          const staged: string[] = await home.listFiles(PER_USER_DIRECTORY_NAME);
          expect(staged).toContain(`${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/newer-only.txt`);
          expect(staged).not.toContain(
            `${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/older-only.txt`,
          );
          expect(await home.read(CONFIG)).toBe(before);
        });
      },
    );
  });
});

describe("updating a home where nothing was ever installed", () => {
  it("stages the runtime and creates no host state at all", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["update"]);
      expect(result.status).toBe(0);

      expect((await home.listFiles(PER_USER_DIRECTORY_NAME)).length).toBeGreaterThan(0);
      for (const relative of await home.list()) {
        expect(relative.startsWith(PER_USER_DIRECTORY_NAME)).toBe(true);
      }
    });
  });

  it("wires nothing even when a host is named", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["update", "claude-code"])).status).toBe(0);
      expect(await home.exists(".claude")).toBe(false);
    });
  });
});
