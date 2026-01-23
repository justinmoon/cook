import { getPublicKey, finalizeEvent, type UnsignedEvent } from "nostr-tools";
import type { Page } from "@playwright/test";

// Test keypair - deterministic for reproducible tests
// NEVER use this in production!
export const TEST_PRIVKEY =
  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

// Derive pubkey using nostr-tools
export const TEST_PUBKEY = getPublicKey(TEST_PRIVKEY);

export interface NostrEvent {
  kind: number;
  created_at: number;
  tags: string[][];
  content: string;
  pubkey?: string;
  id?: string;
  sig?: string;
}

export function signEvent(event: NostrEvent): NostrEvent {
  const unsignedEvent: UnsignedEvent = {
    kind: event.kind,
    created_at: event.created_at,
    tags: event.tags,
    content: event.content,
  };

  // finalizeEvent adds pubkey, id, and sig
  const signedEvent = finalizeEvent(unsignedEvent, TEST_PRIVKEY as `0x${string}`);

  return signedEvent as NostrEvent;
}

/**
 * Sets up NIP-07 mock on a Playwright page.
 * Uses route interception to handle signing in Node.js with real crypto.
 */
export async function setupNostrMock(page: Page): Promise<void> {
  // Set up route to handle signing requests from the injected script
  await page.route("**/__nostr_test_sign__", async (route) => {
    const request = route.request();
    const body = await request.postDataJSON();
    const signedEvent = await signEvent(body.event);

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(signedEvent),
    });
  });

  // Inject the NIP-07 mock script
  await page.addInitScript(`
    const TEST_PUBKEY = "${TEST_PUBKEY}";

    window.nostr = {
      getPublicKey: async () => {
        console.log('[NIP-07 Mock] getPublicKey called');
        return TEST_PUBKEY;
      },

      signEvent: async (event) => {
        console.log('[NIP-07 Mock] signEvent called with:', event);

        // Call our test route to do real signing in Node.js
        const response = await fetch('/__nostr_test_sign__', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ event })
        });

        const signedEvent = await response.json();
        console.log('[NIP-07 Mock] Got signed event:', signedEvent);
        return signedEvent;
      },

      getRelays: async () => ({}),

      nip04: {
        encrypt: async () => { throw new Error('NIP-04 not implemented in mock'); },
        decrypt: async () => { throw new Error('NIP-04 not implemented in mock'); }
      }
    };

    console.log('[NIP-07 Mock] Injected window.nostr with pubkey:', TEST_PUBKEY);
  `);
}
