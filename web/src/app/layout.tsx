import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import Link from "next/link";
import { GitBranch, Settings } from "lucide-react";
import { Button } from "@/components/ui/button";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ThemeToggle } from "@/components/theme-toggle";
import "./globals.css";

// Runs before hydration so there's no light-mode flash for users who
// prefer dark — can't do this with useEffect, since that only runs after
// the initial (wrong) paint.
const THEME_INIT_SCRIPT = `(function(){try{var t=localStorage.getItem("theme");var d=t?t==="dark":window.matchMedia("(prefers-color-scheme: dark)").matches;document.documentElement.classList.toggle("dark",d);}catch(e){}})();`;

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "RunCD",
  description: "ArgoCD-equivalent dashboard for Cloud Run sync units",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      suppressHydrationWarning
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="min-h-full flex flex-col">
        <script dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }} />
        <TooltipProvider delay={200}>
          <header className="bg-card/80 sticky top-0 z-10 border-b backdrop-blur">
            <div className="mx-auto flex w-full max-w-5xl items-center gap-2 px-6 py-3">
              <Link href="/" className="flex flex-1 items-center gap-2">
                <span className="bg-primary text-primary-foreground flex size-7 items-center justify-center rounded-md">
                  <GitBranch className="size-4" />
                </span>
                <span className="font-semibold tracking-tight">RunCD</span>
              </Link>
              <ThemeToggle />
              <Button
                variant="outline"
                size="icon"
                nativeButton={false}
                render={<Link href="/settings" aria-label="Settings" />}
              >
                <Settings className="size-4" />
              </Button>
            </div>
          </header>
          {children}
        </TooltipProvider>
      </body>
    </html>
  );
}
