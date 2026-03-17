import { useState } from "react";
import {
  Button,
  Checkbox,
  Dropdown,
  Input,
  Label,
  Option,
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
    flexWrap: "wrap",
  },
  header: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    borderLeft: `4px solid ${ACTION_COLORS.format.icon}`,
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

export function FormatPanel() {
  const styles = useStyles();
  const { selectedDisks, closePanel, addNotification, updateProgress } = useAppStore();
  const [fileSystem, setFileSystem] = useState("fat32");
  const [label, setLabel] = useState("USB");
  const [quick, setQuick] = useState(true);
  const [loading, setLoading] = useState(false);

  const handleFormat = async () => {
    if (selectedDisks.size === 0) return;
    setLoading(true);
    for (const diskNum of selectedDisks) {
      updateProgress(diskNum, {
        diskNumber: diskNum,
        stage: "Preparing...",
        percentage: 0,
        status: "pending",
      });
    }
    try {
      await go.services.FormatService.StartFormat({
        diskNumbers: [...selectedDisks],
        fileSystem,
        label,
        quick,
      });
      addNotification({
        id: `format-start-${Date.now()}`,
        type: "info",
        message: `Format started on ${selectedDisks.size} drive(s)`,
      });
    } catch (err: any) {
      addNotification({
        id: `format-err-${Date.now()}`,
        type: "error",
        message: `Format failed: ${err?.message || err}`,
      });
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className={styles.form}>
      <div className={styles.header}>
        <Text weight="semibold" size={400}>
          Format USB Drive
        </Text>
        <Button
          appearance="subtle"
          icon={<DismissRegular />}
          onClick={closePanel}
        />
      </div>

      <div className={styles.row}>
        <Label>Filesystem:</Label>
        <Dropdown
          value={fileSystem.toUpperCase()}
          selectedOptions={[fileSystem]}
          onOptionSelect={(_, data) =>
            setFileSystem(data.optionValue ?? "fat32")
          }
          style={{ minWidth: "120px" }}
        >
          <Option value="fat32" text="FAT32">FAT32</Option>
          <Option value="ntfs" text="NTFS">NTFS</Option>
          <Option value="exfat" text="exFAT">exFAT</Option>
        </Dropdown>

        <Label>Label:</Label>
        <Input
          value={label}
          onChange={(_, data) => setLabel(data.value)}
          style={{ width: "160px" }}
        />

        <Checkbox
          label="Quick format"
          checked={quick}
          onChange={(_, data) => setQuick(!!data.checked)}
        />
      </div>

      <div className={styles.actions}>
        <Button appearance="secondary" onClick={closePanel}>
          Cancel
        </Button>
        <Button
          appearance="primary"
          onClick={handleFormat}
          disabled={selectedDisks.size === 0 || loading}
        >
          {loading
            ? "Starting..."
            : `Format ${selectedDisks.size} drive(s)`}
        </Button>
      </div>
    </div>
  );
}
