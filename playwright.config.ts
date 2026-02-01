import { defineConfig, devices } from "@playwright/test";

const RAW_PUBLIC_URL = process.env.COOK_PUBLIC_URL || "";
const E2E_PUBLIC_URL = process.env.COOK_E2E_PUBLIC_URL || "";
const E2E_PORT = process.env.COOK_E2E_PORT || "";
const defaultPublicURL = E2E_PUBLIC_URL || RAW_PUBLIC_URL;
const publicURL = defaultPublicURL ? new URL(defaultPublicURL) : null;
const DEFAULT_PORT = E2E_PORT || publicURL?.port || "7420";
const BASE_URL = process.env.BASE_URL || `http://127.0.0.1:${DEFAULT_PORT}`;
const TEST_PUBKEY = "4646ae5047316b4230d0086c8acec687f00b1cd9d1dc634f6cb358ac0a9a8fff";
const REUSE_SERVER = process.env.COOK_E2E_REUSE === "1";
const baseURL = new URL(BASE_URL);
const SERVER_PORT =
  baseURL.port || (baseURL.protocol === "https:" ? "443" : "80");
let serverPublicURL = defaultPublicURL;
if (!E2E_PUBLIC_URL && RAW_PUBLIC_URL && E2E_PORT) {
  const adjusted = new URL(RAW_PUBLIC_URL);
  adjusted.port = SERVER_PORT;
  serverPublicURL = adjusted.toString();
}

const serverEnv = {
  ...process.env,
  COOK_DATA_DIR: "/tmp/cook-e2e-test",
  COOK_AUTH: "nostr",
  COOK_PORT: SERVER_PORT,
} as Record<string, string>;
if (serverPublicURL) {
  serverEnv.COOK_PUBLIC_URL = serverPublicURL;
}

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: "html",
  use: {
    baseURL: BASE_URL,
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        launchOptions: {
          args: [
            "--no-sandbox",
            "--disable-setuid-sandbox",
            "--disable-gpu",
            "--disable-dev-shm-usage",
            "--no-zygote",
          ],
        },
      },
    },
  ],
  // Run the server before tests if not already running
  webServer: {
    command: `rm -rf /tmp/cook-e2e-test && go run ./cmd/cook repo add ${TEST_PUBKEY}/test && go run ./cmd/cook serve`,
    env: serverEnv,
    url: BASE_URL,
    reuseExistingServer: REUSE_SERVER,
    timeout: 60000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
