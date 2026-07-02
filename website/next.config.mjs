import { createMDX } from 'fumadocs-mdx/next';

const withMDX = createMDX();

// Set DOCS_BASE_PATH (e.g. "/zester") when deploying under a sub-path such as
// GitHub Pages project sites.
const basePath = process.env.DOCS_BASE_PATH || '';

/** @type {import('next').NextConfig} */
const config = {
  output: 'export',
  reactStrictMode: true,
  basePath,
  images: { unoptimized: true },
  env: {
    // client code (e.g. the static search client) can't see basePath at
    // runtime — expose it as an inlined public env var
    NEXT_PUBLIC_BASE_PATH: basePath,
  },
};

export default withMDX(config);
