export interface Preset {
  name: string;
  width: number;
  height: number;
  fps: number;
  bitrate: number;
}

export interface Stats {
  frames_published: number;
  web_subscribers: number;
  session_active: boolean;
}
