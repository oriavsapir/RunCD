import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Produces a minimal, self-contained server (.next/standalone) with only
  // the production dependencies actually used — what the Dockerfile copies
  // into the runtime image instead of the full node_modules tree.
  output: "standalone",
};

export default nextConfig;
