import { useState } from "react";
import {
  Button,
  Checkbox,
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
    flexWrap: "wrap",
  },
  header: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    borderLeft: `4px solid ${ACTION_COLORS.createImage.icon}`,
    paddingLeft: "12px",
    marginLeft: "-16px",
  },
  actions: {
    display: "flex",
    gap: "8px",
    justifyContent: "flex-end",
    marginTop: "8px",
  },
  fileInput: {
    display: "flex",
    gap: "8px",
    alignItems: "center",
    flex: 1,
  },
});

export function CreateImagePanel() {
  const styles = useStyles();
  const { selectedDisks, closePanel, addNotification, updateProgress } = useAppStore();
  const [outputPath, setOutputPath] = useState("");
  const [verify, setVerify] = useState(false);
  const [loading, setLoading] = useState(false);

  // Only one drive can be imaged at a time
  const diskNumber = selectedDisks.size === 1 ? [...selectedDisks][0] : null;

  const handleBrowse = async () => {
    try {
      const path = await go.services.ImageService.OpenSaveDialog();
      if (path) setOutputPath(path);
    } catch (err) {
      console.error("Save dialog failed:", err);
    }
  };

  const handleCreate = async () => {
    if (diskNumber == null || !outputPath) return;
    setLoading(true);
    if (diskNumber != null) {
      updateProgress(diskNumber, {
        diskNumber,
        stage: "Preparing...",
        percentage: 0,
        status: "pending",
      });
    }
    try {
      await go.services.ImageService.StartCreateImage({
        diskNumber,
        outputPath,
        verify,
      });
      addNotification({
        id: `image-start-${Date.now()}`,
        type: "info",
        message: `Image creation started for disk ${diskNumber}`,
      });
    } catch (err: any) {
      addNotification({
        id: `image-err-${Date.now()}`,
        type: "error",
        message: `Image creation failed: ${err?.message || err}`,
      });
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className={styles.form}>
      <div className={styles.header}>
        <Text weight="semibold" size={400}>
          Create Image from USB
        </Text>
        <Button
          appearance="subtle"
          icon={<DismissRegular />}
          onClick={closePanel}
        />
      </div>

      {selectedDisks.size !== 1 && (
        <Text style={{ color: "var(--colorStatusDangerForeground1)" }}>
          Select exactly one drive to create an image.
        </Text>
      )}

      <div className={styles.row}>
        <Label>Output:</Label>
        <div className={styles.fileInput}>
          <Input
            value={outputPath}
            onChange={(_, data) => setOutputPath(data.value)}
            placeholder="Path for .bin image file"
            style={{ flex: 1 }}
          />
          <Button onClick={handleBrowse}>Browse</Button>
        </div>
      </div>

      <div className={styles.row}>
        <Checkbox
          label="Verify after creation"
          checked={verify}
          onChange={(_, data) => setVerify(!!data.checked)}
        />
      </div>

      <div className={styles.actions}>
        <Button appearance="secondary" onClick={closePanel}>
          Cancel
        </Button>
        <Button
          appearance="primary"
          onClick={handleCreate}
          disabled={diskNumber == null || !outputPath || loading}
        >
          {loading ? "Starting..." : "Create Image"}
        </Button>
      </div>
    </div>
  );
}
