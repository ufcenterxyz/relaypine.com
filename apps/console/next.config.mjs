/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
  async rewrites() {
    const gateway = process.env.GATEWAY_URL || "http://127.0.0.1:4811";
    return [{ source: "/api/:path*", destination: `${gateway}/api/:path*` }];
  },
};
export default nextConfig;
