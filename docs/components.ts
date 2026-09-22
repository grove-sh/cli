import { defineComponents } from "blume";

// Parsed statically by blume rather than executed, so the values stay literal.
export default defineComponents({
  layout: {
    Layout: "./components/Layout.astro",
    PageHeader: "./components/PageHeader.astro",
  },
});
