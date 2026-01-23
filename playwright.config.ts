import { defineConfig, devices } from "@playwright/test";

const BASE_URL = process.env.BASE_URL || "http://127.0.0.1:7420";

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
    command: "rm -rf /tmp/cook-e2e-test && COOK_DATA_DIR=/tmp/cook-e2e-test COOK_AUTH=nostr go run ./cmd/cook serve",
    url: BASE_URL,
    reuseExistingServer: false, // Always start fresh for reproducible tests
    timeout: 60000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
