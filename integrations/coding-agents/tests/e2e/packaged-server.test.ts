import { describe, expect, it } from "vitest";
import { copyFile, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { HOOK_SCRIPT_FILES } from "../../src/hosts/lifecycle";
import { MCP_SERVER_FILE_NAME, PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME } from "../../src/runtime/layout";
import { NO_HOST_CLI, packageRoot, runInstaller, type FakeHome, withFakeHome, withRestrictedPath, writeStandInDsh } from "../helpers/fake-home";



/**
 * The guarantee between staging and the MCP entries that name the server.
 *
 * Every host's server entry is the absolute path of the packaged server, so an
 * install whose runtime did not stage that file would wire hosts whose memory
 * tools never start. Install refuses that, and it refuses it before any host
 * is wired, so the failure leaves no half-wired host behind.
 */

/** The three hook entry points an older package release could have shipped. */
const HOOK_FILES: string[] = Object.values(HOOK_SCRIPT_FILES).map((file) => `hooks/${file}`);

/** A stand-in package: the real CLI, the hook entry points, and optionally the server. */
async function fakePackage(includeServer: boolean): Promise<string> {
  const root: string = await mkdtemp(join(tmpdir(), "wenyuan-fake-package-"));
  await mkdir(join(root, "dist", "cli"), { recursive: true });
  await copyFile(
    join(packageRoot(), "dist", "cli", "main.mjs"),
    join(root, "dist", "cli", "main.mjs"),
  );
  const staged: string[] = includeServer ? [...HOOK_FILES, MCP_SERVER_FILE_NAME] : HOOK_FILES;
  for (const file of staged) {
    const target: string = join(root, "dist", "runtime", file);
    await mkdir(join(target, ".."), { recursive: true });
    await writeFile(target, `// ${file}\n`);
  }
  await mkdir(join(root, "skill"), { recursive: true });
  await writeFile(join(root, "skill", "SKILL.md"), "# companion skill\n");
  return root;
}

async function withPackage<T>(includeServer: boolean, body: (root: string) => Promise<T>): Promise<T> {
  const root: string = await fakePackage(includeServer);
  try {
    return await body(root);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

describe("a runtime that did not stage the packaged server", () => {
  it("refuses the install and names the missing file", async () => {
    await withPackage(false, async (root: string) => {
      await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async () => {
        const result = await runInstaller(["install", "dsh"], { packageRoot: root });
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain(MCP_SERVER_FILE_NAME);
      });
    });
  });

  it("leaves no host wired at all, rather than a host with an absent server", async () => {
    await withPackage(false, async (root: string) => {
      await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
        await runInstaller(["install", "dsh"], { packageRoot: root });
        // Not even the hook config: the refusal happens before any host adapter
        // runs, and staging is the only write that survives it.
        expect(await home.exists(".dsh/hooks/wenyuan.json")).toBe(false);
        expect(await home.exists(".dsh/mcp.json")).toBe(false);
        expect(await home.exists(`${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/hooks`)).toBe(true);
      });
    });
  });

  it("wires the host once the runtime does stage it", async () => {
    await withPackage(true, async (root: string) => {
      await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
        const bin: string = await writeStandInDsh(home);
        const result = await withRestrictedPath([bin], () => runInstaller(["install", "dsh"], { packageRoot: root }));
        expect(result.status).toBe(0);
        expect(
          await home.isFile(`${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/${MCP_SERVER_FILE_NAME}`),
        ).toBe(true);
      });
    });
  });
});