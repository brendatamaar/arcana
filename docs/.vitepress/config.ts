import { defineConfig } from "vitepress";

// Published to GitHub Pages as a project site; override the base for other hosting.
const base = process.env.DOCS_BASE ?? "/arcana/";
const repo = "https://github.com/brendatamaar/arcana";

export default defineConfig({
  title: "Arcana",
  description: "A self-hosted sandbox orchestrator that lets AI agents run untrusted code safely.",
  base,
  cleanUrls: true,
  lastUpdated: true,
  // Project-internal planning documents stay in the repository and out of the published site.
  srcExclude: ["AGENTS.md", "PLAN.md", "PRD.md", "node_modules/**", "tapes/**", "benchmarks/*.md"],
  themeConfig: {
    nav: [
      { text: "Get started", link: "/quickstart" },
      { text: "Guides", link: "/dashboard" },
      { text: "Operate", link: "/configuration" },
      { text: "Security", link: "/security" },
      { text: "Architecture", link: "/architecture" },
    ],
    sidebar: [
      { text: "Get started", items: [{ text: "Quickstart", link: "/quickstart" }, { text: "Install", link: "/install" }] },
      {
        text: "Use Arcana",
        items: [
          { text: "Web dashboard", link: "/dashboard" },
          { text: "MCP server", link: "/mcp" },
          { text: "SDKs", link: "/sdks" },
          { text: "Events and sessions", link: "/events" },
        ],
      },
      {
        text: "Operate",
        items: [
          { text: "Configuration", link: "/configuration" },
          { text: "Operations", link: "/operations" },
          { text: "Networking", link: "/networking" },
          { text: "Rate limits", link: "/rate-limits" },
          { text: "Audit log", link: "/audit" },
        ],
      },
      { text: "Security", items: [{ text: "Security model", link: "/security" }, { text: "Escape suite", link: "/escape-v1" }] },
      { text: "Performance", items: [{ text: "Benchmarks", link: "/benchmarks" }, { text: "Reference agent results", link: "/agent-results" }] },
      { text: "Design", items: [{ text: "Architecture", link: "/architecture" }, { text: "Decision records", link: "/decisions" }] },
    ],
    socialLinks: [{ icon: "github", link: repo }],
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: "Edit this page on GitHub" },
    search: { provider: "local" },
    footer: { message: "Released under the Apache-2.0 License.", copyright: "Arcana" },
  },
  // The OpenAPI contract and project documents live in the repository, not the site.
  ignoreDeadLinks: [/^\.\.\//, /\/(PRD|PLAN|AGENTS)(\.md)?$/, /^\.\/(PRD|PLAN|AGENTS)/],
});
