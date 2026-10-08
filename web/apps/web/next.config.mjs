import path from 'node:path';

/** Public website (Next.js SSR, PRD EP-12). Branding is fetched from the API at runtime. */
const nextConfig = {
  output: 'standalone',
  outputFileTracingRoot: path.join(import.meta.dirname, '../..'),
  // the captured pages of the live site are read at runtime (components/mgcc)
  outputFileTracingIncludes: { '/[lang]/**': ['./mgcc/**'] },
  productionBrowserSourceMaps: process.env.SOURCEMAPS !== 'false',
  transpilePackages: ['@oneclub/ui'],
  async rewrites() {
    return [{ source: '/api/:path*', destination: `${process.env.ONECLUB_API_URL ?? 'http://localhost:8080'}/api/:path*` }];
  },
};

export default nextConfig;
