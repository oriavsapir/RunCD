"use client";

import { Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";

// No React state for the dark/light bit — both icons render and CSS
// (dark:) shows the right one, matching whatever class the anti-flash
// script in layout.tsx already put on <html>. Avoids a hydration-mismatch
// state sync (there's no way to read document.documentElement in a lazy
// useState initializer without disagreeing with the server-rendered pass).
function toggle() {
  const next = !document.documentElement.classList.contains("dark");
  document.documentElement.classList.toggle("dark", next);
  localStorage.setItem("theme", next ? "dark" : "light");
}

export function ThemeToggle() {
  return (
    <Button
      variant="outline"
      size="icon"
      aria-label="Toggle dark mode"
      onClick={toggle}
    >
      <Sun className="size-4 dark:hidden" />
      <Moon className="hidden size-4 dark:block" />
    </Button>
  );
}
