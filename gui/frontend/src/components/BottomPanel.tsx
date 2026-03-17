import { makeStyles, tokens } from "@fluentui/react-components";
import type { ReactNode } from "react";

const useStyles = makeStyles({
  container: {
    borderTop: `1px solid ${tokens.colorNeutralStroke1}`,
    backgroundColor: tokens.colorNeutralBackground2,
    padding: "16px",
    transition: "max-height 0.3s ease, padding 0.3s ease",
    overflow: "hidden",
  },
  hidden: {
    maxHeight: "0px",
    padding: "0 16px",
    borderTop: "none",
  },
  visible: {
    maxHeight: "400px",
  },
});

interface BottomPanelProps {
  open: boolean;
  children: ReactNode;
}

export function BottomPanel({ open, children }: BottomPanelProps) {
  const styles = useStyles();

  return (
    <div
      className={`${styles.container} ${open ? styles.visible : styles.hidden}`}
    >
      {open && children}
    </div>
  );
}
