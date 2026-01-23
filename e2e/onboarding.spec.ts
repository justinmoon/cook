import { test, expect } from "@playwright/test";
import { setupNostrMock, TEST_PUBKEY } from "./lib/nostr-mock";

// Test SSH key (ed25519) - this is a throwaway test key, not for production use
const TEST_SSH_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJqroFRwqhT3rl/7MjKi6PiFOcHZYlLMzUndKwp8Lv7m test@example.com";

test.describe("Full onboarding flow", () => {
  test.beforeEach(async ({ page }) => {
    // Set up NIP-07 mock before each test
    await setupNostrMock(page);
  });

  test("complete onboarding: login -> SSH key -> create repo", async ({
    page,
  }) => {
    // Step 1: Login
    await test.step("Login with Nostr", async () => {
      await page.goto("/login");
      await expect(page.locator("h1")).toContainText("Cook");

      await page.click('button:has-text("Login with Nostr")');

      // Wait for redirect to dashboard
      await page.waitForURL("**/dashboard", { timeout: 15000 });
      await expect(page.locator("h1")).toContainText("Dashboard");

      // Verify user identity is shown
      const shortPubkey = TEST_PUBKEY.slice(0, 8);
      await expect(page.locator("body")).toContainText(shortPubkey);
    });

    // Step 2: Verify SSH key is required before creating repos
    await test.step("SSH key required message shown", async () => {
      // Should see message about adding SSH key
      await expect(
        page.locator('text="Add an SSH key to get started."')
      ).toBeVisible();

      // Create repo section should show disabled message
      await expect(
        page.locator('text="Add an SSH key above to enable repository creation."')
      ).toBeVisible();
    });

    // Step 3: Add SSH key
    await test.step("Add SSH key", async () => {
      // The details is already open when there are no SSH keys
      // Just fill in the form directly

      // Fill in the SSH key
      await page.fill('textarea[name="key"]', TEST_SSH_KEY);
      await page.fill('input[name="name"]', "Test Key");

      // Submit the form
      await page.click('button:has-text("Add SSH Key")');

      // Wait for page to reload and show the key
      await page.waitForURL("**/dashboard");

      // Verify key appears on the page
      await expect(page.getByText("Test Key")).toBeVisible();
      await expect(page.getByText("SHA256:")).toBeVisible();
    });

    // Step 4: Create repository (should now be enabled)
    await test.step("Create repository", async () => {
      // The "Add SSH key to get started" message should be gone
      await expect(
        page.locator('text="Add an SSH key to get started."')
      ).not.toBeVisible();

      // Fill in repo creation form (use the form's specific input)
      const repoForm = page.locator('#repo-form');
      await repoForm.locator('input[name="name"]').fill("test-repo");

      // Submit
      await repoForm.locator('button:has-text("Create Repository")').click();

      // Should redirect to repo detail page
      await page.waitForURL("**/repos/**");
      await expect(page.locator("h1")).toContainText("test-repo");
    });

    // Step 5: Create a task in the repo
    await test.step("Create task", async () => {
      // Expand create task form
      const taskDetails = page.locator("details:has(summary:has-text('Create Task'))");
      await taskDetails.locator("summary").click();

      // Fill in task
      await page.fill('input[name="title"]', "Implement feature X");
      await page.fill('textarea[name="body"]', "This is a test task description");

      // Submit
      await page.click('button:has-text("Create Task")');

      // Page should reload and show the task
      await page.waitForURL("**/repos/**");
      // Check that the task link appears on the page
      await expect(page.getByRole('link', { name: 'implement-feature-x' })).toBeVisible();
    });

    // Step 6: Create a branch
    await test.step("Create branch", async () => {
      // Expand create branch form
      const branchDetails = page.locator("details:has(summary:has-text('Create Branch'))");
      await branchDetails.locator("summary").click();

      // Fill in branch name
      await page.fill('input[name="name"]', "feature-x");

      // Link to the task we just created
      await page.selectOption('select[name="task_slug"]', "implement-feature-x");

      // Submit
      await page.click('button:has-text("Create Branch")');

      // Page should reload and show the branch
      await page.waitForURL("**/repos/**");
      // The branches table is first on the page
      await expect(page.locator("table").first()).toContainText("feature-x");
    });

    // Step 7: Verify navigation works
    await test.step("Navigation shows logged-in state", async () => {
      // Nav should show Dashboard link
      await expect(page.locator('nav a:has-text("Dashboard")')).toBeVisible();

      // Nav should show Logout
      await expect(page.locator('nav a:has-text("Logout")')).toBeVisible();

      // Click Dashboard to go back
      await page.click('nav a:has-text("Dashboard")');
      await page.waitForURL("**/dashboard");

      // Should see our repo in the list (repos table)
      await expect(page.getByRole('link', { name: 'test-repo' })).toBeVisible();
    });

    // Step 8: Logout
    await test.step("Logout", async () => {
      await page.click('a:has-text("Logout")');
      await page.waitForURL("**/login");

      // Should be on login page
      await expect(page.locator('button:has-text("Login with Nostr")')).toBeVisible();
    });
  });
});
