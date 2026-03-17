import { useState } from "react";
import {
  Button,
  Input,
  Label,
  Text,
  makeStyles,
} from "@fluentui/react-components";
import { DismissRegular } from "@fluentui/react-icons";
import { useAppStore } from "../store/appStore";
import { ACTION_COLORS } from "../actionColors";

const go = (window as any).go;

const useStyles = makeStyles({
  form: {
    display: "flex",
    flexDirection: "column",
    gap: "12px",
  },
  row: {
    display: "flex",
    alignItems: "center",
    gap: "12px",
  },
  header: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    borderLeft: `4px solid ${ACTION_COLORS.label.icon}`,
    paddingLeft: "12px",
    marginLeft: "-16px",
  },
  actions: {
    display: "flex",
    gap: "8px",
    justifyContent: "flex-end",
    marginTop: "8px",
  },
});

export function LabelPanel() {
  const styles = useStyles();
  const { selectedDisks, closePanel, addNotification } = useAppStore();
  const [label, setLabel] = useState("");
  const [loading, setLoading] = useState(false);

  const handleSetLabel = async () => {
    if (selectedDisks.size === 0 || !label) return;
    setLoading(true);
    try {
      await go.services.DiskService.SetLabels({
        diskNumbers: [...selectedDisks],
        label,
      });
      addNotification({
        id: `label-ok-${Date.now()}`,
        type: "success",
        message: `Label set to "${label}" on ${selectedDisks.size} drive(s)`,
      });
      closePanel();
    } catch (err: any) {
      addNotification({
        id: `label-err-${Date.now()}`,
        type: "error",
        message: `Label failed: ${err?.message || err}`,
      });
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className={styles.form}>
      <div className={styles.header}>
        <Text weight="semibold" size={400}>
          Set Volume Label
        </Text>
        <Button
          appearance="subtle"
          icon={<DismissRegular />}
          onClick={closePanel}
        />
      </div>

      <div className={styles.row}>
        <Label>Label:</Label>
        <Input
          value={label}
          onChange={(_, data) => setLabel(data.value)}
          placeholder="New volume label"
          style={{ width: "240px" }}
        />
      </div>

      <div className={styles.actions}>
        <Button appearance="secondary" onClick={closePanel}>
          Cancel
        </Button>
        <Button
          appearance="primary"
          onClick={handleSetLabel}
          disabled={selectedDisks.size === 0 || !label || loading}
        >
          {loading
            ? "Setting..."
            : `Label ${selectedDisks.size} drive(s)`}
        </Button>
      </div>
    </div>
  );
}
