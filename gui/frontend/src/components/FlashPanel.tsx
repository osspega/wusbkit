import { useState } from "react";
import {
  Button,
  Checkbox,
  Dropdown,
  Input,
  Label,
  Option,
  makeStyles,
  tokens,
  Text,
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
    borderLeft: `4px solid ${ACTION_COLORS.flash.icon}`,
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

const BUFFER_SIZES = ["1", "2", "4", "8", "16", "32", "64"];

export function FlashPanel() {
  const styles = useStyles();
  const { selectedDisks, closePanel, addNotification, lastImagePath, setLastImagePath, updateProgress } = useAppStore();
  const [imagePath, setImagePath] = useState(lastImagePath);
  const [verify, setVerify] = useState(false);
  const [calculateHash, setCalculateHash] = useState(false);
  const [skipUnchanged, setSkipUnchanged] = useState(false);
  const [bufferSize, setBufferSize] = useState("4");
  const [loading, setLoading] = useState(false);

  const handleBrowse = async () => {
    try {
      const path = await go.services.FlashService.OpenImageDialog();
      if (path) setImagePath(path);
    } catch (err) {
      console.error("Browse failed:", err);
    }
  };

  const handleFlash = async () => {
    if (selectedDisks.size === 0 || !imagePath) return;
    setLoading(true);
    setLastImagePath(imagePath);
    for (const diskNum of selectedDisks) {
      updateProgress(diskNum, {
        diskNumber: diskNum,
        stage: "Preparing...",
        percentage: 0,
        status: "pending",
      });
    }
    try {
      await go.services.FlashService.StartFlash({
        diskNumbers: [...selectedDisks],
        imagePath,
        verify,
        calculateHash,
        skipUnchanged,
        bufferSizeMB: parseInt(bufferSize),
      });
      addNotification({
        id: `flash-start-${Date.now()}`,
        type: "info",
        message: `Flash started on ${selectedDisks.size} drive(s)`,
      });
    } catch (err: any) {
      addNotification({
        id: `flash-err-${Date.now()}`,
        type: "error",
        message: `Flash failed: ${err?.message || err}`,
      });
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className={styles.form}>
      <div className={styles.header}>
        <Text weight="semibold" size={400}>
          Flash Image to USB
        </Text>
        <Button
          appearance="subtle"
          icon={<DismissRegular />}
          onClick={closePanel}
        />
      </div>

      <div className={styles.row}>
        <Label>Image:</Label>
        <div className={styles.fileInput}>
          <Input
            value={imagePath}
            onChange={(_, data) => setImagePath(data.value)}
            placeholder="Path to image file or URL"
            style={{ flex: 1 }}
          />
          <Button onClick={handleBrowse}>Browse</Button>
        </div>
      </div>

      <div className={styles.row}>
        <Checkbox
          label="Verify after write"
          checked={verify}
          onChange={(_, data) => setVerify(!!data.checked)}
        />
        <Checkbox
          label="Calculate SHA-256"
          checked={calculateHash}
          onChange={(_, data) => setCalculateHash(!!data.checked)}
        />
        <Checkbox
          label="Skip unchanged sectors"
          checked={skipUnchanged}
          onChange={(_, data) => setSkipUnchanged(!!data.checked)}
        />
        <Label>Buffer:</Label>
        <Dropdown
          value={bufferSize + " MB"}
          selectedOptions={[bufferSize]}
          onOptionSelect={(_, data) => setBufferSize(data.optionValue ?? "4")}
          style={{ minWidth: "90px" }}
        >
          {BUFFER_SIZES.map((s) => (
            <Option key={s} value={s} text={`${s} MB`}>
              {s} MB
            </Option>
          ))}
        </Dropdown>
      </div>

      <div className={styles.actions}>
        <Button appearance="secondary" onClick={closePanel}>
          Cancel
        </Button>
        <Button
          appearance="primary"
          onClick={handleFlash}
          disabled={selectedDisks.size === 0 || !imagePath || loading}
        >
          {loading
            ? "Starting..."
            : `Flash ${selectedDisks.size} drive(s)`}
        </Button>
      </div>
    </div>
  );
}
