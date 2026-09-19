import { defineConfig } from "blume";

export default defineConfig({
  title: "grove",
  description:
    "Local HTTPS hostnames, port allocation, and env vars, scoped per git worktree.",
  // The banner is a wordmark, so the header shows it alone.
  logo: {
    image: {
      light: "/grove-banner-light.svg",
      dark: "/grove-banner-dark.svg",
      alt: "grove",
    },
    text: "",
    href: "https://grov.site",
  },
  content: {
    root: "content",
  },
  github: {
    owner: "grove-sh",
    repo: "cli",
    dir: "docs",
  },
  theme: {
    // grov.site's palette: fern on spruce in the dark, and the deep fern that
    // still reads on a light ground, since fern itself is 1.70:1 on birch.
    accent: { light: "#3c782c", dark: "#7fc96b" },
    background: { light: "#fbfaf6", dark: "#14170f" },
    radius: "sm",
    fonts: {
      display: { name: "Fraunces", weights: ["100..900"] },
      body: "inter",
      mono: "jetbrains-mono",
    },
  },
  lastModified: true,
  navigation: {
    sidebar: {
      display: "flat",
    },
    tabs: [
      { label: "Docs", path: "/" },
      { label: "GitHub", path: "https://github.com/grove-sh/cli" },
      { label: "npm", path: "https://www.npmjs.com/package/@grove-sh/cli" },
    ],
  },
  deployment: {
    // GitHub Pages does not expose the origin at build time.
    site: "https://docs.grov.site",
  },
});
