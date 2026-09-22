import { defineConfig } from "blume";

export default defineConfig({
  title: "grove",
  description:
    "An HTTPS hostname, leased ports, and env vars for every git worktree, so every branch you or an agent is working on can run at once.",
  logo: {
    image: {
      light: "/grove-banner-light.svg",
      dark: "/grove-banner-dark.svg",
      alt: "grove",
    },
    text: "",
  },
  content: {
    root: "content",
  },
  // The site root is the landing page in pages/index.astro; every generated
  // docs route lives under this. Content links are still written from root.
  basePath: "/docs",
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
    // Self-hosted from the fontsource packages rather than fetched from
    // Google at build: that fetch failed often enough to fail CI on a retry.
    // Static faces at the weights the pages render, not the variable file:
    // blume preloads by exact weight (body 400/500, display 500, mono 400) and
    // a range matches none of them, so nothing was preloaded and every first
    // paint swapped fonts. An italic per weight is free, since a face is only
    // fetched when something asks for it.
    fonts: {
      display: {
        name: "Source Serif 4",
        fallback: "serif",
        variants: [
          { src: "./node_modules/@fontsource/source-serif-4/files/source-serif-4-latin-500-normal.woff2", weight: 500 },
          { src: "./node_modules/@fontsource/source-serif-4/files/source-serif-4-latin-500-italic.woff2", weight: 500, style: "italic" },
        ],
      },
      body: {
        name: "Inter",
        variants: [
          { src: "./node_modules/@fontsource/inter/files/inter-latin-400-normal.woff2", weight: 400 },
          { src: "./node_modules/@fontsource/inter/files/inter-latin-400-italic.woff2", weight: 400, style: "italic" },
          { src: "./node_modules/@fontsource/inter/files/inter-latin-500-normal.woff2", weight: 500 },
          { src: "./node_modules/@fontsource/inter/files/inter-latin-500-italic.woff2", weight: 500, style: "italic" },
          { src: "./node_modules/@fontsource/inter/files/inter-latin-600-normal.woff2", weight: 600 },
          { src: "./node_modules/@fontsource/inter/files/inter-latin-600-italic.woff2", weight: 600, style: "italic" },
        ],
      },
      mono: {
        name: "JetBrains Mono",
        fallback: "mono",
        variants: [
          { src: "./node_modules/@fontsource/jetbrains-mono/files/jetbrains-mono-latin-400-normal.woff2", weight: 400 },
          { src: "./node_modules/@fontsource/jetbrains-mono/files/jetbrains-mono-latin-400-italic.woff2", weight: 400, style: "italic" },
          { src: "./node_modules/@fontsource/jetbrains-mono/files/jetbrains-mono-latin-500-normal.woff2", weight: 500 },
          { src: "./node_modules/@fontsource/jetbrains-mono/files/jetbrains-mono-latin-500-italic.woff2", weight: 500, style: "italic" },
        ],
      },
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
    // site: "https://grov.site",
    site: "https://fe8f-46-110-132-221.ngrok-free.app",
  },
  seo: {
    // One card for the whole site, public/og.png, pointed at from
    // components/Layout.astro and pages/index.astro. Turning blume's per-page
    // cards back on needs an `og.fonts` list: the renderer takes the family
    // name unquoted, and "Source Serif 4" is not a valid bare font-family
    // because of the digit, so every card fails to render without one.
    og: { enabled: false },
    // Identity for the agents that read JSON-LD before recommending a tool.
    // Both need deployment.site, since their @ids are absolute.
    organization: {
      name: "grove",
      sameAs: ["https://github.com/grove-sh"],
    },
    software: {
      // schema.org wants a URL or a CreativeWork here, not the SPDX name.
      license: "https://www.apache.org/licenses/LICENSE-2.0",
      operatingSystem: "macOS, Linux",
      // An Offer of 0 is how schema.org says free; omitting price says nothing.
      price: 0,
      sameAs: [
        "https://github.com/grove-sh/cli",
        "https://www.npmjs.com/package/@grove-sh/cli",
      ],
    },
  },
  integrations: [
    {
      name: "grove-vite",
      hooks: {
        "astro:config:setup": ({ updateConfig }) => {
          updateConfig({
            vite: {
              server: { allowedHosts: [".ngrok-free.app"] },
              build: {
                rollupOptions: {
                  onwarn(warning, warn) {
                    if (
                      warning.code === "MODULE_LEVEL_DIRECTIVE" &&
                      warning.message.includes("astro:head-inject")
                    ) {
                      return;
                    }
                    warn(warning);
                  },
                },
              },
            },
          });
        },
      },
    },
  ],
});
