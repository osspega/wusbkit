export const ACTION_COLORS = {
  flash: {
    icon: "#E07B39",
    tint: "rgba(224, 123, 57, 0.08)",
    active: "rgba(224, 123, 57, 0.14)",
  },
  format: {
    icon: "#CC4A1F",
    tint: "rgba(204, 74, 31, 0.08)",
    active: "rgba(204, 74, 31, 0.14)",
  },
  createImage: {
    icon: "#0E7A6E",
    tint: "rgba(14, 122, 110, 0.08)",
    active: "rgba(14, 122, 110, 0.14)",
  },
  label: {
    icon: "#7B5EA7",
    tint: "rgba(123, 94, 167, 0.08)",
    active: "rgba(123, 94, 167, 0.14)",
  },
  eject: {
    icon: "#2A7DB5",
    tint: "rgba(42, 125, 181, 0.08)",
    active: "rgba(42, 125, 181, 0.14)",
  },
} as const;

export type ActionType = keyof typeof ACTION_COLORS;

export function getProgressColor(stage: string): string {
  const s = stage.toLowerCase();
  if (s.includes("flash") || s.includes("writ")) return ACTION_COLORS.flash.icon;
  if (s.includes("format")) return ACTION_COLORS.format.icon;
  if (s.includes("image") || s.includes("read")) return ACTION_COLORS.createImage.icon;
  return "#2A7DB5";
}
