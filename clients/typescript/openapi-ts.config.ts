import { defineConfig, type UserConfig } from "@hey-api/openapi-ts";

// The generator input is this project's own derived document, which sits beside
// this directory. Regenerate it with `scripts/derive_client_spec.py` when the
// served document changes; `scripts/generate_clients.sh typescript` does both.
//
// `client` selects the runtime the generated SDK calls through; it is a
// documented option that the pinned generator's own types do not declare, so the
// literal is asserted to the config type once, here.
const config = {
  input: "../openapi.json",
  output: {
    path: "./generated",
    postProcess: ["prettier"],
  },
  client: "@hey-api/client-fetch",
  plugins: ["@hey-api/typescript", "@hey-api/sdk"],
} as UserConfig;

export default defineConfig(config);