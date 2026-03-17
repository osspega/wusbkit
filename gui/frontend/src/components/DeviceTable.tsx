import {
  DataGrid,
  DataGridBody,
  DataGridCell,
  DataGridHeader,
  DataGridHeaderCell,
  DataGridRow,
  TableColumnDefinition,
  createTableColumn,
  Checkbox,
  ProgressBar,
  Badge,
  Text,
  Tooltip,
  tokens,
  makeStyles,
} from "@fluentui/react-components";
import type { DeviceDTO, OperationProgress } from "../types";
import { useAppStore } from "../store/appStore";
import { getProgressColor } from "../actionColors";
import "../shimmer.css";

const useStyles = makeStyles({
  newRow: {
    backgroundColor: tokens.colorBrandBackground2,
    transitionProperty: "background-color",
    transitionDuration: "1s",
    transitionTimingFunction: "ease-out",
  },
  clickableRow: {
    cursor: "pointer",
    ":hover": {
      backgroundColor: tokens.colorNeutralBackground1Hover,
    },
  },
  truncateCell: {
    overflow: "hidden",
    textOverflow: "ellipsis",
    whiteSpace: "nowrap",
  },
  progressCell: {
    display: "flex",
    flexDirection: "column",
    gap: "2px",
    width: "100%",
  },
  progressHeader: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "baseline",
  },
  progressFooter: {
    display: "flex",
    justifyContent: "space-between",
  },
  shimmerWrapper: {
    position: "relative",
    overflow: "hidden",
    borderRadius: "4px",
  },
  shimmerOverlay: {
    position: "absolute",
    top: "0",
    left: "0",
    width: "25%",
    height: "100%",
    backgroundImage:
      "linear-gradient(90deg, transparent 0%, rgba(255,255,255,0.25) 50%, transparent 100%)",
    animationName: "shimmerSlide",
    animationDuration: "2s",
    animationTimingFunction: "ease-in-out",
    animationIterationCount: "infinite",
    pointerEvents: "none",
  },
  sizeBar: {
    height: "3px",
    borderRadius: "2px",
    backgroundColor: tokens.colorNeutralStroke2,
    marginTop: "2px",
  },
  sizeBarFill: {
    height: "100%",
    borderRadius: "2px",
    backgroundColor: tokens.colorBrandForeground2,
  },
});

const COLUMN_WIDTHS: Record<string, React.CSSProperties> = {
  select: { width: 40, maxWidth: 40, minWidth: 40 },
  diskNumber: { width: 60, maxWidth: 60, minWidth: 60 },
  driveLetter: { width: 60, maxWidth: 60, minWidth: 60 },
  friendlyName: { minWidth: 120, flex: 1, overflow: "hidden" },
  serialNumber: { minWidth: 100, flex: 1, overflow: "hidden" },
  size: { width: 90, maxWidth: 90, minWidth: 90 },
  fileSystem: { width: 60, maxWidth: 60, minWidth: 60 },
  status: { minWidth: 180, flex: 1 },
};

const columns: TableColumnDefinition<DeviceDTO>[] = [
  createTableColumn<DeviceDTO>({
    columnId: "select",
    renderHeaderCell: () => "",
    renderCell: () => null,
  }),
  createTableColumn<DeviceDTO>({
    columnId: "diskNumber",
    compare: (a, b) => a.diskNumber - b.diskNumber,
    renderHeaderCell: () => "Disk#",
    renderCell: (item) => item.diskNumber,
  }),
  createTableColumn<DeviceDTO>({
    columnId: "driveLetter",
    compare: (a, b) => a.driveLetter.localeCompare(b.driveLetter),
    renderHeaderCell: () => "Drive",
    renderCell: (item) => item.driveLetter || "-",
  }),
  createTableColumn<DeviceDTO>({
    columnId: "friendlyName",
    compare: (a, b) => a.friendlyName.localeCompare(b.friendlyName),
    renderHeaderCell: () => "Name",
    renderCell: (item) => item.friendlyName || item.model,
  }),
  createTableColumn<DeviceDTO>({
    columnId: "serialNumber",
    compare: (a, b) => a.serialNumber.localeCompare(b.serialNumber),
    renderHeaderCell: () => "Serial",
    renderCell: (item) => item.serialNumber || "-",
  }),
  createTableColumn<DeviceDTO>({
    columnId: "size",
    compare: (a, b) => a.size - b.size,
    renderHeaderCell: () => "Size",
    renderCell: (item) => item.sizeHuman,
  }),
  createTableColumn<DeviceDTO>({
    columnId: "fileSystem",
    compare: (a, b) => a.fileSystem.localeCompare(b.fileSystem),
    renderHeaderCell: () => "FS",
    renderCell: (item) => item.fileSystem || "-",
  }),
  createTableColumn<DeviceDTO>({
    columnId: "status",
    renderHeaderCell: () => "Status",
    renderCell: () => null,
  }),
];

interface DeviceTableProps {
  devices: DeviceDTO[];
  newDiskNumbers: Set<number>;
}

export function DeviceTable({ devices, newDiskNumbers }: DeviceTableProps) {
  const styles = useStyles();
  const { selectedDisks, toggleSelection, activeOperations } = useAppStore();

  const maxSize = devices.reduce((max, d) => Math.max(max, d.size), 0);

  return (
    <DataGrid
      items={devices}
      columns={columns}
      sortable
      getRowId={(item) => String(item.diskNumber)}
      style={{ flex: 1, overflow: "auto" }}
    >
      <DataGridHeader>
        <DataGridRow>
          {({ columnId, renderHeaderCell }) => (
            <DataGridHeaderCell
              key={columnId}
              style={COLUMN_WIDTHS[columnId]}
            >
              {renderHeaderCell()}
            </DataGridHeaderCell>
          )}
        </DataGridRow>
      </DataGridHeader>
      <DataGridBody<DeviceDTO>>
        {({ item, rowId }) => {
          const isNew = newDiskNumbers.has(item.diskNumber);
          const isSelected = selectedDisks.has(item.diskNumber);
          const progress = activeOperations.get(item.diskNumber);

          return (
            <DataGridRow<DeviceDTO>
              key={rowId}
              className={`${styles.clickableRow} ${isNew ? styles.newRow : ""}`}
              onClick={() => toggleSelection(item.diskNumber)}
            >
              {({ columnId, renderCell }) => (
                <DataGridCell
                  key={columnId}
                  style={COLUMN_WIDTHS[columnId]}
                >
                  {columnId === "select" ? (
                    <Checkbox
                      checked={isSelected}
                      onChange={(e) => {
                        e.stopPropagation();
                        toggleSelection(item.diskNumber);
                      }}
                      onClick={(e) => e.stopPropagation()}
                    />
                  ) : columnId === "friendlyName" ? (
                    <TruncatedCell text={item.friendlyName || item.model} />
                  ) : columnId === "serialNumber" ? (
                    <TruncatedCell text={item.serialNumber || "-"} />
                  ) : columnId === "size" ? (
                    <SizeCell
                      sizeHuman={item.sizeHuman}
                      size={item.size}
                      maxSize={maxSize}
                    />
                  ) : columnId === "status" ? (
                    <StatusCell
                      device={item}
                      progress={progress}
                      isNew={isNew}
                    />
                  ) : (
                    renderCell(item)
                  )}
                </DataGridCell>
              )}
            </DataGridRow>
          );
        }}
      </DataGridBody>
    </DataGrid>
  );
}

function TruncatedCell({ text }: { text: string }) {
  const styles = useStyles();
  return (
    <Tooltip content={text} relationship="label" positioning="above">
      <span className={styles.truncateCell}>{text}</span>
    </Tooltip>
  );
}

function SizeCell({
  sizeHuman,
  size,
  maxSize,
}: {
  sizeHuman: string;
  size: number;
  maxSize: number;
}) {
  const styles = useStyles();
  const pct = maxSize > 0 ? (size / maxSize) * 100 : 0;

  return (
    <div>
      <Text size={200}>{sizeHuman}</Text>
      <div className={styles.sizeBar}>
        <div
          className={styles.sizeBarFill}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function StatusCell({
  device,
  progress,
  isNew,
}: {
  device: DeviceDTO;
  progress?: OperationProgress;
  isNew: boolean;
}) {
  const styles = useStyles();

  if (progress && progress.status === "pending") {
    const color = getProgressColor(progress.stage);
    return (
      <div className={styles.progressCell}>
        <ProgressBar
          thickness="large"
          style={
            {
              "--fui-ProgressBar--indicator-color": color,
            } as React.CSSProperties
          }
        />
        <Text
          size={100}
          style={{ color: tokens.colorNeutralForeground3 }}
        >
          Preparing...
        </Text>
      </div>
    );
  }

  if (progress) {
    const color = getProgressColor(progress.stage);
    const isActive = progress.percentage > 0 && progress.percentage < 100;

    return (
      <div className={styles.progressCell}>
        <div className={styles.progressHeader}>
          <Text
            size={100}
            style={{ color: tokens.colorNeutralForeground2 }}
          >
            {progress.stage}
          </Text>
          <Text
            size={300}
            weight="bold"
            style={{ color }}
          >
            {progress.percentage}%
          </Text>
        </div>
        <div className={styles.shimmerWrapper}>
          <ProgressBar
            value={progress.percentage / 100}
            thickness="large"
            style={
              {
                "--fui-ProgressBar--indicator-color": color,
              } as React.CSSProperties
            }
          />
          {isActive && <div className={styles.shimmerOverlay} />}
        </div>
        <div className={styles.progressFooter}>
          <Text
            size={100}
            style={{ color: tokens.colorNeutralForeground3 }}
          >
            {progress.speed ?? ""}
          </Text>
        </div>
      </div>
    );
  }

  return (
    <span>
      {device.status || "Online"}
      {isNew && (
        <>
          {" "}
          <Badge appearance="filled" color="informative" size="small">
            NEW
          </Badge>
        </>
      )}
    </span>
  );
}
