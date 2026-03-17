import { useState, useEffect } from "react";
import { webDarkTheme, webLightTheme } from "@fluentui/react-components";
import type { Theme } from "@fluentui/react-components";

export function useSystemTheme(): Theme {
  const [isDark, setIsDark] = useState(
    () => window.matchMedia("(prefers-color-scheme: dark)").matches
  );

  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const handler = (e: MediaQueryListEvent) => setIsDark(e.matches);
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, []);

  return isDark ? webDarkTheme : webLightTheme;
}
