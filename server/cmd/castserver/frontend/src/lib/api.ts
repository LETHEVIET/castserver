export interface Preset {
  name: string;
  width: number;
  height: number;
  fps: number;
  bitrate: number;
  jpeg_quality: number;
  scaler: string;
  chunk_ms: number;
  hardware_accel: boolean;
}

export interface Stats {
  frames_published: number;
  web_subscribers: number;
  session_active: boolean;
}
