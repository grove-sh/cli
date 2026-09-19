import { defineComponents } from "blume";

// Parsed statically by blume rather than executed, so the values stay literal.
export default defineComponents({
  layout: {
    PageHeader: "./components/PageHeader.astro",
  },
});
