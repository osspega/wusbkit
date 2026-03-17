import {
  Toolbar,
  ToolbarButton,
  ToolbarDivider,
  Text,
  MessageBar,
  MessageBarBody,
  MessageBarActions,
  Button,
  Dialog,
  DialogTrigger,
  DialogSurface,
  DialogTitle,
  DialogBody,
  DialogContent,
  DialogActions,
  makeStyles,
  tokens,
} from "@fluentui/react-components";
import {
  ArrowSyncRegular,
  FlashRegular,
  HardDriveRegular,
  TagRegular,
  ArrowEjectRegular,
  ImageRegular,
  SelectAllOnRegular,
} from "@fluentui/react-icons";
import { useState } from "react";
import { useAppStore } from "../store/appStore";
import { ACTION_COLORS } from "../actionColors";
import { useDevices } from "../hooks/useDevices";
import { useOperationProgress } from "../hooks/useOperationProgress";
import { DeviceTable } from "./DeviceTable";
import { BottomPanel } from "./BottomPanel";
import { FlashPanel } from "./FlashPanel";
import { FormatPanel } from "./FormatPanel";
import { LabelPanel } from "./LabelPanel";
import { CreateImagePanel } from "./CreateImagePanel";

const go = (window as any).go;

const useStyles = makeStyles({
  root: {
    display: "flex",
    flexDirection: "column",
    height: "100vh",
    overflow: "hidden",
  },
  toolbar: {
    borderBottom: `1px solid ${tokens.colorNeutralStroke1}`,
    padding: "4px 8px",
    flexShrink: 0,
  },
  content: {
    flex: 1,
    overflow: "auto",
    display: "flex",
    flexDirection: "column",
  },
  statusBar: {
    borderTop: `1px solid ${tokens.colorNeutralStroke1}`,
    padding: "4px 12px",
    flexShrink: 0,
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    backgroundColor: tokens.colorNeutralBackground2,
  },
  notifications: {
    padding: "0 8px",
    display: "flex",
    flexDirection: "column",
    gap: "4px",
  },
  flashBtn: {
    ":hover": { backgroundColor: ACTION_COLORS.flash.tint },
    ":active": { backgroundColor: ACTION_COLORS.flash.active },
  },
  formatBtn: {
    ":hover": { backgroundColor: ACTION_COLORS.format.tint },
    ":active": { backgroundColor: ACTION_COLORS.format.active },
  },
  createImageBtn: {
    ":hover": { backgroundColor: ACTION_COLORS.createImage.tint },
    ":active": { backgroundColor: ACTION_COLORS.createImage.active },
  },
  labelBtn: {
    ":hover": { backgroundColor: ACTION_COLORS.label.tint },
    ":active": { backgroundColor: ACTION_COLORS.label.active },
  },
  ejectBtn: {
    ":hover": { backgroundColor: ACTION_COLORS.eject.tint },
    ":active": { backgroundColor: ACTION_COLORS.eject.active },
  },
});

export function AppLayout() {
  const styles = useStyles();
  const {
    selectedDisks,
    currentPanel,
    openPanel,
    closePanel,
    activeOperations,
    notifications,
    dismissNotification,
    selectAll,
    deselectAll,
    devices: storeDevices,
    addNotification,
  } = useAppStore();
  const { devices, newDiskNumbers, refresh } = useDevices();
  const [refreshing, setRefreshing] = useState(false);
  const [ejectDialogOpen, setEjectDialogOpen] = useState(false);

  // Register progress event listeners
  useOperationProgress();

  const handleRefresh = async () => {
    setRefreshing(true);
    await refresh();
    setRefreshing(false);
  };

  const handleEject = async () => {
    setEjectDialogOpen(false);
    for (const diskNum of selectedDisks) {
      try {
        await go.services.DiskService.EjectDisk(diskNum);
        addNotification({
          id: `eject-ok-${diskNum}-${Date.now()}`,
          type: "success",
          message: `Disk ${diskNum} ejected`,
        });
      } catch (err: any) {
        addNotification({
          id: `eject-err-${diskNum}-${Date.now()}`,
          type: "error",
          message: `Eject disk ${diskNum} failed: ${err?.message || err}`,
        });
      }
    }
    await refresh();
  };

  const allSelected =
    devices.length > 0 && selectedDisks.size === devices.length;

  return (
    <div className={styles.root}>
      {/* Toolbar */}
      <Toolbar className={styles.toolbar}>
        <ToolbarButton
          icon={<ArrowSyncRegular />}
          onClick={handleRefresh}
          disabled={refreshing}
        >
          Refresh
        </ToolbarButton>
        <ToolbarButton
          icon={<SelectAllOnRegular />}
          onClick={allSelected ? deselectAll : selectAll}
        >
          {allSelected ? "Deselect All" : "Select All"}
        </ToolbarButton>
        <ToolbarDivider />
        <ToolbarButton
          className={styles.flashBtn}
          icon={<FlashRegular style={{ color: ACTION_COLORS.flash.icon }} />}
          onClick={() => openPanel("flash")}
          disabled={selectedDisks.size === 0}
        >
          Flash
        </ToolbarButton>
        <ToolbarButton
          className={styles.formatBtn}
          icon={<HardDriveRegular style={{ color: ACTION_COLORS.format.icon }} />}
          onClick={() => openPanel("format")}
          disabled={selectedDisks.size === 0}
        >
          Format
        </ToolbarButton>
        <ToolbarButton
          className={styles.createImageBtn}
          icon={<ImageRegular style={{ color: ACTION_COLORS.createImage.icon }} />}
          onClick={() => openPanel("createImage")}
          disabled={selectedDisks.size !== 1}
        >
          Create Image
        </ToolbarButton>
        <ToolbarButton
          className={styles.labelBtn}
          icon={<TagRegular style={{ color: ACTION_COLORS.label.icon }} />}
          onClick={() => openPanel("label")}
          disabled={selectedDisks.size === 0}
        >
          Label
        </ToolbarButton>
        <ToolbarDivider />
        <ToolbarButton
          className={styles.ejectBtn}
          icon={<ArrowEjectRegular style={{ color: ACTION_COLORS.eject.icon }} />}
          onClick={() => setEjectDialogOpen(true)}
          disabled={selectedDisks.size === 0}
        >
          Eject
        </ToolbarButton>
      </Toolbar>

      {/* Notifications */}
      {notifications.length > 0 && (
        <div className={styles.notifications}>
          {notifications.map((n) => (
            <MessageBar
              key={n.id}
              intent={
                n.type === "success"
                  ? "success"
                  : n.type === "error"
                    ? "error"
                    : n.type === "warning"
                      ? "warning"
                      : "info"
              }
            >
              <MessageBarBody>{n.message}</MessageBarBody>
              <MessageBarActions>
                <Button
                  appearance="transparent"
                  size="small"
                  onClick={() => dismissNotification(n.id)}
                >
                  Dismiss
                </Button>
              </MessageBarActions>
            </MessageBar>
          ))}
        </div>
      )}

      {/* Device Table */}
      <div className={styles.content}>
        <DeviceTable devices={devices} newDiskNumbers={newDiskNumbers} />
      </div>

      {/* Bottom Panel */}
      <BottomPanel open={currentPanel !== null}>
        {currentPanel === "flash" && <FlashPanel />}
        {currentPanel === "format" && <FormatPanel />}
        {currentPanel === "label" && <LabelPanel />}
        {currentPanel === "createImage" && <CreateImagePanel />}
      </BottomPanel>

      {/* Status Bar */}
      <div className={styles.statusBar}>
        <Text size={200}>
          {devices.length} drive{devices.length !== 1 ? "s" : ""} connected
          {selectedDisks.size > 0 &&
            ` | ${selectedDisks.size} selected`}
        </Text>
        {activeOperations.size > 0 && (
          <Text size={200}>
            {activeOperations.size} operation
            {activeOperations.size !== 1 ? "s" : ""} in progress
          </Text>
        )}
      </div>

      {/* Eject Confirmation Dialog */}
      <Dialog open={ejectDialogOpen} onOpenChange={(_, data) => setEjectDialogOpen(data.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Eject USB Drive(s)</DialogTitle>
            <DialogContent>
              Are you sure you want to eject {selectedDisks.size} drive(s)?
              This will safely remove the hardware.
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button appearance="secondary">Cancel</Button>
              </DialogTrigger>
              <Button appearance="primary" onClick={handleEject}>
                Eject
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
