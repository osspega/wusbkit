import { useEffect } from "react";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { useAppStore } from "../store/appStore";
import type { OperationProgress } from "../types";

function playCompletionSound() {
  try {
    const ctx = new AudioContext();
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.frequency.value = 880;
    osc.type = "sine";
    gain.gain.value = 0.15;
    gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.3);
    osc.start(ctx.currentTime);
    osc.stop(ctx.currentTime + 0.3);
  } catch {
    // Audio not available — ignore
  }
}

function playErrorSound() {
  try {
    const ctx = new AudioContext();
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.frequency.value = 300;
    osc.type = "square";
    gain.gain.value = 0.1;
    gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.4);
    osc.start(ctx.currentTime);
    osc.stop(ctx.currentTime + 0.4);
  } catch {
    // Audio not available — ignore
  }
}

export function useOperationProgress() {
  const { updateProgress, clearProgress, addNotification } = useAppStore();

  useEffect(() => {
    const cleanups: Array<() => void> = [];

    const handleProgress =
      (opType: string) => (data: OperationProgress) => {
        if (data.status === "complete") {
          clearProgress(data.diskNumber);
          playCompletionSound();
          addNotification({
            id: `${opType}-${data.diskNumber}-${Date.now()}`,
            type: "success",
            message: `${opType} complete on disk ${data.diskNumber}${data.hash ? ` (SHA-256: ${data.hash.slice(0, 12)}...)` : ""}`,
          });
        } else if (data.status === "error") {
          clearProgress(data.diskNumber);
          playErrorSound();
          addNotification({
            id: `${opType}-err-${data.diskNumber}-${Date.now()}`,
            type: "error",
            message: `${opType} failed on disk ${data.diskNumber}: ${data.error}`,
          });
        } else {
          updateProgress(data.diskNumber, data);
        }
      };

    const events = [
      ["flash:progress", "Flash"],
      ["format:progress", "Format"],
      ["image:progress", "Image"],
    ] as const;

    for (const [event, label] of events) {
      const unsub = EventsOn(event, handleProgress(label));
      cleanups.push(unsub);
    }

    return () => {
      cleanups.forEach((fn) => fn());
    };
  }, [updateProgress, clearProgress, addNotification]);
}
