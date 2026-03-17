import { FluentProvider } from "@fluentui/react-components";
import { useSystemTheme } from "./hooks/useSystemTheme";
import { AppLayout } from "./components/AppLayout";

export function App() {
  const theme = useSystemTheme();

  return (
    <FluentProvider theme={theme} style={{ height: "100vh" }}>
      <AppLayout />
    </FluentProvider>
  );
}
