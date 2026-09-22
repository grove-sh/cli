import { ogHeight, ogVersion, ogWidth } from "./og-version.ts";

// Unfurl caches key a card by its URL and there is no way to ask them to drop
// one, so the query carries the card's digest, stamped by scripts/og-card.sh:
// it moves only when the image does, and a rebuild that leaves the card alone
// keeps the caches that already have it.
export const ogImagePath = `/og.png?v=${ogVersion}`;

export const ogImageSize = { height: ogHeight, width: ogWidth };
