/** @type {import('next').NextConfig} */
const nextConfig = {
  // Static export: the dashboard ships as plain files inside the Go binary.
  // This is what makes `//go:embed` possible, and it is why there is no
  // Next.js runtime in production: the Go process serves the assets and the
  // /api/v1 routes, and nothing else executes JavaScript on a server.
  output: 'export',

  // Asset paths must be root-relative. The dashboard is served from the root
  // of the Go server, so a basePath would only break asset resolution.
  // assetPrefix: '',

  // Emit `out/index.html` rather than `out/index.txt`-style fallbacks, and keep
  // the directory layout the Go file server expects.
  trailingSlash: false,

  // The export is deterministic: no build-time timestamps in the output, so
  // two builds of the same source produce the same bytes.
  generateBuildId: async () => 'lensyxe',

  reactStrictMode: true,
  poweredByHeader: false,
};

module.exports = nextConfig;