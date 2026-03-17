export interface DeviceDTO {
  diskNumber: number;
  driveLetter: string;
  friendlyName: string;
  model: string;
  serialNumber: string;
  size: number;
  sizeHuman: string;
  fileSystem: string;
  volumeLabel: string;
  partitionStyle: string;
  status: string;
  healthStatus: string;
  busType: string;
  locationInfo: string;
}

export interface FlashOptionsDTO {
  diskNumbers: number[];
  imagePath: string;
  verify: boolean;
  calculateHash: boolean;
  skipUnchanged: boolean;
  bufferSizeMB: number;
}

export interface FormatOptionsDTO {
  diskNumbers: number[];
  fileSystem: string;
  label: string;
  quick: boolean;
}

export interface LabelOptionsDTO {
  diskNumbers: number[];
  label: string;
}

export interface CreateImageOptionsDTO {
  diskNumber: number;
  outputPath: string;
  verify: boolean;
}

export interface OperationProgress {
  diskNumber: number;
  stage: string;
  percentage: number;
  speed?: string;
  status: string;
  error?: string;
  // flash-specific
  bytesWritten?: number;
  totalBytes?: number;
  hash?: string;
  bytesSkipped?: number;
  // format-specific
  driveLetter?: string;
  // image-specific
  bytesRead?: number;
}

export type PanelType = "flash" | "format" | "createImage" | "label" | null;
