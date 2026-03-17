import { create } from "zustand";
import type { DeviceDTO, OperationProgress, PanelType } from "../types";

interface AppState {
  devices: DeviceDTO[];
  previousDiskNumbers: Set<number>;
  newDiskNumbers: Set<number>;
  selectedDisks: Set<number>;
  activeOperations: Map<number, OperationProgress>;
  currentPanel: PanelType;
  notifications: Notification[];
  lastImagePath: string;

  setDevices: (devices: DeviceDTO[]) => void;
  markNewDrives: (diskNumbers: Set<number>) => void;
  clearNewDrives: () => void;
  toggleSelection: (diskNumber: number) => void;
  selectAll: () => void;
  deselectAll: () => void;
  openPanel: (panel: PanelType) => void;
  closePanel: () => void;
  updateProgress: (diskNumber: number, progress: OperationProgress) => void;
  clearProgress: (diskNumber: number) => void;
  addNotification: (notification: Notification) => void;
  dismissNotification: (id: string) => void;
  setLastImagePath: (path: string) => void;
}

export interface Notification {
  id: string;
  type: "success" | "error" | "info" | "warning";
  message: string;
}

export const useAppStore = create<AppState>((set) => ({
  devices: [],
  previousDiskNumbers: new Set(),
  newDiskNumbers: new Set(),
  selectedDisks: new Set(),
  activeOperations: new Map(),
  currentPanel: null,
  notifications: [],
  lastImagePath: "",

  setDevices: (devices) =>
    set((state) => {
      const currentDiskNumbers = new Set(devices.map((d) => d.diskNumber));
      // Keep selection only for drives that still exist
      const validSelection = new Set(
        [...state.selectedDisks].filter((d) => currentDiskNumbers.has(d))
      );
      return {
        devices,
        previousDiskNumbers: currentDiskNumbers,
        selectedDisks: validSelection,
      };
    }),

  markNewDrives: (diskNumbers) => set({ newDiskNumbers: diskNumbers }),
  clearNewDrives: () => set({ newDiskNumbers: new Set() }),

  toggleSelection: (diskNumber) =>
    set((state) => {
      const next = new Set(state.selectedDisks);
      if (next.has(diskNumber)) {
        next.delete(diskNumber);
      } else {
        next.add(diskNumber);
      }
      return { selectedDisks: next };
    }),

  selectAll: () =>
    set((state) => ({
      selectedDisks: new Set(state.devices.map((d) => d.diskNumber)),
    })),

  deselectAll: () => set({ selectedDisks: new Set() }),

  openPanel: (panel) => set({ currentPanel: panel }),
  closePanel: () => set({ currentPanel: null }),

  updateProgress: (diskNumber, progress) =>
    set((state) => {
      const next = new Map(state.activeOperations);
      next.set(diskNumber, progress);
      return { activeOperations: next };
    }),

  clearProgress: (diskNumber) =>
    set((state) => {
      const next = new Map(state.activeOperations);
      next.delete(diskNumber);
      return { activeOperations: next };
    }),

  addNotification: (notification) =>
    set((state) => ({
      notifications: [...state.notifications, notification],
    })),

  dismissNotification: (id) =>
    set((state) => ({
      notifications: state.notifications.filter((n) => n.id !== id),
    })),

  setLastImagePath: (path) => set({ lastImagePath: path }),
}));
