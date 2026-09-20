import { defineConfig } from "blume";

export default defineConfig({
  title: "grove",
  description:
    "Local HTTPS hostnames, port allocation, and env vars, scoped per git worktree.",
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
    // Each is the variable Latin file, so one @font-face covers every weight.
    fonts: {
      display: {
        name: "Source Serif 4",
        fallback: "serif",
        variants: [
          { src: "./node_modules/@fontsource-variable/source-serif-4/files/source-serif-4-latin-wght-normal.woff2", weight: "200..900" },
          { src: "./node_modules/@fontsource-variable/source-serif-4/files/source-serif-4-latin-wght-italic.woff2", weight: "200..900", style: "italic" },
        ],
      },
      body: {
        name: "Inter",
        variants: [
          { src: "./node_modules/@fontsource-variable/inter/files/inter-latin-wght-normal.woff2", weight: "100..900" },
          { src: "./node_modules/@fontsource-variable/inter/files/inter-latin-wght-italic.woff2", weight: "100..900", style: "italic" },
        ],
      },
      mono: {
        name: "JetBrains Mono",
        fallback: "mono",
        variants: [
          { src: "./node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2", weight: "100..800" },
          { src: "./node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-italic.woff2", weight: "100..800", style: "italic" },
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
    site: "https://grov.site",
  },
  seo: {
    // Rendered at build. The card is always dark, so it takes the dark palette.
    og: {
      logo: "/grove-banner-dark.svg",
      // Cards would follow the theme's fonts on their own, but blume hands
      // the renderer the family name unquoted, and "Source Serif 4" is not
      // a valid bare font-family because of the digit: the build fails on
      // every card. Listing the font here takes a different path that works,
      // at the cost of the title/body split, so the whole card is serif:
      // bold for the title, regular for the rest. Local files, one weight
      // each, since the renderer takes no variable font and fetching from
      // Google is the flake the site fonts above avoid.
      fonts: [
        { name: "Source Serif 4", src: "./node_modules/@fontsource/source-serif-4/files/source-serif-4-latin-400-normal.woff2", weight: 400 },
        { name: "Source Serif 4", src: "./node_modules/@fontsource/source-serif-4/files/source-serif-4-latin-700-normal.woff2", weight: 700 },
      ],
      site: "grov.site",
      palette: {
        accent: "#7fc96b",
        background: "#14170f",
        foreground: "#f0ece3",
        muted: "#8fa394",
        border: "#2b2b2b",
      },
    },
  },
  integrations: [
    {
      name: "grove-vite",
      hooks: {
        "astro:config:setup": ({ updateConfig }) => {
          updateConfig({
            vite: {
              // server: { allowedHosts: [".ngrok-free.app"] },
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
