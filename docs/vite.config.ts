import mdx from "@mdx-js/rollup";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import rehypeHighlight from "rehype-highlight";
import rehypeSlug from "rehype-slug";
import remarkGfm from "remark-gfm";

const docsRoot = path.dirname(fileURLToPath(import.meta.url));
const mdxCompiler = mdx({
  providerImportSource: "@mdx-js/react",
  remarkPlugins: [remarkGfm],
  rehypePlugins: [rehypeSlug, rehypeHighlight],
});
const compileMdx = mdxCompiler.transform as (
  this: unknown,
  code: string,
  id: string,
) => unknown;
const mdxPlugin = {
  ...mdxCompiler,
  enforce: "pre",
  transform(this: unknown, code: string, id: string) {
    if (id.includes("?raw")) return null;
    return compileMdx.call(this, code, id);
  },
} as Plugin;

export default defineConfig({
  plugins: [
    mdxPlugin,
    react({ include: /\.(js|jsx|mdx|ts|tsx)$/ }),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(docsRoot, "src"),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The local full-text index intentionally carries every document's raw source.
    chunkSizeWarningLimit: 750,
  },
});
