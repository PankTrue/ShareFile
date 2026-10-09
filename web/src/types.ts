export type User = {
  id: string;
  username: string;
  admin: boolean;
  active: boolean;
  home_id: string;
};
export type Session = { user: User; csrf_token: string; admin_listener: boolean };
export type UIInfo = { admin_listener: boolean; chunk_bytes: number; upload_slots: number };
export type FileNode = {
  id: string;
  parent_id?: string;
  owner_id: string;
  name: string;
  kind: 'file' | 'folder';
  size: number;
  created_at: number;
  deleted_at?: number;
  effective_role?: number;
};
export type Share = {
  id: string;
  node_id: string;
  user_id: string;
  created_at: number;
  expires_at?: number;
  revoked: boolean;
  password_protected: boolean;
};
export type Group = { id: string; name: string };
export type Grant = {
  principal_type: 'user' | 'group';
  principal_id: string;
  principal_name?: string;
  role: number;
};
export type Principal = { id: string; type: 'user' | 'group'; name: string };
export type Device = {
  id: string;
  name: string;
  created_at: number;
  expires_at: number;
  revoked: boolean;
};
export type DeviceRequest = { id: string; name: string; expires_at: number };
export type Settings = {
  upload_ttl_seconds: number;
  chunk_bytes: number;
  upload_slots: number;
  download_slots: number;
  reserve_bytes: number;
  import_roots: number;
};
export type ServerConfig = {
  data_dir: string;
  volume_id: string;
  public_addr: string;
  public_url: string;
  admin_addr: string;
  admin_url: string;
  admin_cert: string;
  admin_key: string;
  web_dir: string;
  import_dirs: string[];
  reserve_bytes: number;
  upload_slots: number;
  download_slots: number;
  chunk_bytes: number;
  log_file: string;
  insecure_local: boolean;
};
export type Configuration = {
  active: ServerConfig;
  pending: ServerConfig | null;
  revision: string;
  applying: boolean;
  last_error: string;
};
export type Volume = {
  path: string;
  device: string;
  filesystem: string;
  total_bytes: number;
  free_bytes: number;
  read_only: boolean;
  current: boolean;
};
