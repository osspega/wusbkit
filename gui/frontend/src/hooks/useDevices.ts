import { useCallback, useEffect, useRef } from "react";
import { useAppStore } from "../store/appStore";
import type { DeviceDTO } from "../types";

// Wails generates these bindings
declare function ListDevices(): Promise<DeviceDTO[]>;

// Access the Go bindings via the global wails runtime
const go = (window as any).go;

export function useDevices() {
  const {
    devices,
    previousDiskNumbers,
    newDiskNumbers,
    setDevices,
    markNewDrives,
    clearNewDrives,
  } = useAppStore();

  const fadeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const refresh = useCallback(async () => {
    try {
      const fresh: DeviceDTO[] =
        await go.services.DeviceService.ListDevices();

      // Detect new drives
      const freshDiskNumbers = new Set(fresh.map((d) => d.diskNumber));
      const newDrives = new Set<number>();
      for (const dn of freshDiskNumbers) {
        if (!previousDiskNumbers.has(dn)) {
          newDrives.add(dn);
        }
      }

      setDevices(fresh);

      if (newDrives.size > 0) {
        markNewDrives(newDrives);

        // Clear badge after 5 seconds
        if (fadeTimerRef.current) clearTimeout(fadeTimerRef.current);
        fadeTimerRef.current = setTimeout(() => {
          clearNewDrives();
          fadeTimerRef.current = null;
        }, 5000);
      }
    } catch (err) {
      console.error("Failed to list devices:", err);
    }
  }, [previousDiskNumbers, setDevices, markNewDrives, clearNewDrives]);

  // Load devices on mount
  useEffect(() => {
    refresh();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return { devices, newDiskNumbers, refresh };
}
